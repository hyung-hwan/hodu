
## normal operation
- ./hodu server --rpc-on=0.0.0.0:9999 --ctl-on=0.0.0.0:8888 --pxy-on=0.0.0.0:9998 --wpx-on=0.0.0.0:9997
- ./hodu client --rpc-to=127.0.0.1:9999 --ctl-on=127.0.0.1:7777 "127.0.0.2:22,0.0.0.0:12345,ssh,Access SSH Server"

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


### Add a new route


`clinet-route.json` contains the following text:

```
{
    "id": 0,
    "client-peer-addr": "192.168.1.104:22",
    "client-peer-name": "Star gate",
    "server-peer-option": "tcp4 ssh",
    "server-peer-svc-addr": "0.0.0.0:0",
    "server-peer-svc-net": "",
    "lifetime": "0"
}
```

Run this command:
```
curl -X POST --data-binary @client-route.json http://127.0.0.1:7777/_ctl/client-conns/1/routes
```
