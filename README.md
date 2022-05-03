# imap-forward

A simple tool that copies/moves messages from one IMAP mailbox (folder) to another.

It supports copying the messages as a one-time operation, or as a daemon (using IDLE for low-latency syncing).

## Usage

To copy messages from inbox `foo@example.com` to `bar@other.example.com` once:
```shell
imap-forward -upstream-url example.com:993 -upstream-username foo@example.com -upstream-password himom -downstream-url other.example.com:993 -downstream-username bar@other.example.com -downstream-password hidad
```

To continuously move/forward messages from inbox `foo@example.com` to `bar@other.example.com`:
```shell
imap-forward -upstream-url example.com:993 -upstream-username foo@example.com -upstream-password himom -downstream-url other.example.com:993 -downstream-username bar@other.example.com -downstream-password hidad -move -sync
```

## Acknowledgements

This is just 200 LoC over [go-imap](https://github.com/emersion/go-imap) ;)

## License

MIT
