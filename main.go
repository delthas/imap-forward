package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"log"
	"os"
	"sync"
	"time"
)

func fetchForEach(c *client.Client, start int, end int, fetchItems []imap.FetchItem, f func(message *imap.Message) error) error {
	var set imap.SeqSet
	set.AddRange(uint32(start), uint32(end))
	m := make(chan *imap.Message, end-start)
	done := make(chan error, 1)
	go func() {
		done <- c.Fetch(&set, fetchItems, m)
	}()
	for msg := range m {
		if err := f(msg); err != nil {
			return err
		}
	}
	return <-done
}

func remove(c *client.Client, start int, end int) error {
	var set imap.SeqSet
	set.AddRange(uint32(start), uint32(end))
	item := imap.FormatFlagsOp(imap.AddFlags, true)
	flags := []interface{}{imap.DeletedFlag}
	if err := c.Store(&set, item, flags, nil); err != nil {
		return err
	}
	if err := c.Expunge(nil); err != nil {
		return err
	}
	return nil
}

type Config struct {
	upstreamURL        string
	upstreamUsername   string
	upstreamPassword   string
	upstreamFolder     string
	downstreamURL      string
	downstreamUsername string
	downstreamPassword string
	downstreamFolder   string
	sync               bool
	move               bool
	verbose            bool
}

var logOut = log.New(os.Stdout, "", 0)
var logErr = log.New(os.Stderr, "err: ", 0)

func main() {
	var c Config
	flag.StringVar(&c.upstreamURL, "upstream-url", "", "upstream server url")
	flag.StringVar(&c.upstreamUsername, "upstream-username", "", "upstream server username")
	flag.StringVar(&c.upstreamPassword, "upstream-password", "", "upstream server password")
	flag.StringVar(&c.upstreamFolder, "upstream-folder", imap.InboxName, "upstream folder")
	flag.StringVar(&c.downstreamURL, "downstream-url", "", "downstream server url")
	flag.StringVar(&c.downstreamUsername, "downstream-username", "", "downstream server username")
	flag.StringVar(&c.downstreamPassword, "downstream-password", "", "downstream server password")
	flag.StringVar(&c.downstreamFolder, "downstream-folder", "", "downstream folder (defaults to upstream folder)")
	flag.BoolVar(&c.sync, "sync", false, "continuously sync mails rather than copying once")
	flag.BoolVar(&c.move, "move", false, "move existing messages instead of copying")
	flag.BoolVar(&c.verbose, "verbose", false, "print debug logs to stdout")
	flag.Parse()
	if c.downstreamFolder == "" {
		c.downstreamFolder = c.upstreamFolder
	}

	for {
		err := run(&c)
		if err != nil {
			logErr.Println(err)
		} else {
			break
		}
		time.Sleep(1 * time.Minute)
	}
}

func run(c *Config) error {
	dc, err := client.DialTLS(c.downstreamURL, &tls.Config{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return fmt.Errorf("dialing downstream: %v", err)
	}
	defer dc.Logout()
	if err := dc.Login(c.downstreamUsername, c.downstreamPassword); err != nil {
		return fmt.Errorf("login to downstream: %v", err)
	}
	// select the mailbox to check if it exists; if it does not, create it.
	if _, err := dc.Select(c.downstreamFolder, true); err != nil {
		if err := dc.Create(c.downstreamFolder); err != nil {
			return fmt.Errorf("creating downstream mailbox: %v", err)
		}
	}

	uc, err := client.DialTLS(c.upstreamURL, &tls.Config{
		InsecureSkipVerify: true,
	})
	if err != nil {
		return fmt.Errorf("dialing upstream: %v", err)
	}
	defer uc.Logout()
	if err := uc.Login(c.upstreamUsername, c.upstreamPassword); err != nil {
		return fmt.Errorf("login to upstream: %v", err)
	}
	mbox, err := uc.Select(c.upstreamFolder, !c.move)
	if err != nil {
		return fmt.Errorf("selecting upstream mailbox: %v", err)
	}

	updateCond := sync.Cond{
		L: &sync.Mutex{},
	}
	updates := make(chan client.Update, 64)
	updatesClose := make(chan struct{}, 1)
	uc.Updates = updates
	defer close(updatesClose)
	defer updateCond.Broadcast()

	lastCount := 0
	newCount := int(mbox.Messages)

	go func() {
		for {
			select {
			case update := <-updates:
				updateCond.L.Lock()
				switch update := update.(type) {
				case *client.MailboxUpdate:
					newCount = int(update.Mailbox.Messages)
					if c.verbose {
						logOut.Printf("adding messages: upstream now has %v messages", newCount)
					}
				case *client.ExpungeUpdate:
					lastCount--
					newCount--
					if c.verbose {
						logOut.Printf("removing message: upstream now has %v messages", newCount)
					}
				}
				updateCond.Broadcast()
				updateCond.L.Unlock()
			case <-updatesClose:
				return
			}
		}
	}()

	for {
		updateCond.L.Lock()
		newCountLocal := newCount
		lastCountLocal := lastCount
		updateCond.L.Unlock()

		if newCountLocal > lastCountLocal {
			if c.verbose {
				logOut.Printf("processing upstream messages %v to %v", lastCountLocal+1, newCountLocal)
			}

			bodySection := &imap.BodySectionName{
				Peek: true,
			}
			items := []imap.FetchItem{imap.FetchFlags, imap.FetchInternalDate, imap.FetchRFC822Size, imap.FetchEnvelope, imap.FetchBody, bodySection.FetchItem()}

			err := fetchForEach(uc, lastCountLocal+1, newCountLocal, items, func(msg *imap.Message) error {
				if c.verbose {
					logOut.Printf("appending message %v to downstream", int(msg.SeqNum))
				}
				if err := dc.Append(c.downstreamFolder, msg.Flags, msg.InternalDate, msg.GetBody(bodySection)); err != nil {
					return fmt.Errorf("appending message to downstream: %v", err)
				}
				return nil
			})
			if err != nil {
				return fmt.Errorf("fetching upstream messages: %v", err)
			}
			updateCond.L.Lock()
			lastCount = newCountLocal
			updateCond.L.Unlock()
			if c.move {
				if err := remove(uc, lastCountLocal+1, newCountLocal); err != nil {
					return fmt.Errorf("deleting upstream messages: %v", err)
				}
			}
			if c.verbose {
				logOut.Printf("processed upstream messages, upstream now has %v messages", newCountLocal)
			}
		}
		if !c.sync {
			return nil
		}
		errCh := make(chan error, 1)
		updateCh := make(chan struct{}, 1)
		idleCh := make(chan struct{})
		go func() {
			errCh <- uc.Idle(idleCh, nil)
		}()
		go func() {
			updateCond.L.Lock()
			updateCond.Wait()
			updateCond.L.Unlock()
			updateCh <- struct{}{}
		}()
		select {
		case err := <-errCh:
			if err != nil {
				return err
			}
		case <-updateCh:
		}
		close(idleCh)
	}
}
