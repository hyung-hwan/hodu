
## normal operation
- ./hodu server --listen-on=0.0.0.0:9999 --listen-on=0.0.0.0:8888
- ./hodu client --listen-on=127.0.0.1:7777 --server=127.0.0.1:9999 192.168.1.130:8000

## server.json
```
{
    "server-addr": "127.0.0.1:9999",
    "peer-addrs": [
        "127.0.0.1:22",
        "127.0.0.1:80"
    ]
}
```

## client control channel

```
curl -X POST --data-binary @server.json http://127.0.0.1:7777/servers
```
