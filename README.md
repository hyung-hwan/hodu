## pty and rpty access

- ./hodu server --rpc-on=0.0.0.0:9999 --ctl-on=[::1]:8888 --pty-shell=/bin/bash
- ./hodu client --rpc-to=127.0.0.1:9999 --ctl-on=[::1]:1107 --client-token=test-client --pty-shell=/bin/bash

On the server-side:
 - Access `http://[::1]:8888/_rpty/xterm.html?client-token=test-client` using a web browser to access a pty session of a remote client.
 - Access `http://[::1]:8888/_pty/xterm.html` using a web browser to access a local pty session.

On the client-side:
 - Access `http://[::1]:1107/_pty/xterm.html` using a web browser to access a local pty session.

## port based proxy service
- ./hodu server --rpc-on=0.0.0.0:9999 --ctl-on=0.0.0.0:8888 --pxy-on=0.0.0.0:9998 --wpx-on=0.0.0.0:9997 --rpx-on=0.0.0.0:9996
- ./hodu client --rpc-to=127.0.0.1:9999 --ctl-on=127.0.0.1:1107 --client-token=tratra --rpx-target-addr=127.0.0.1:1212 "127.0.0.2:22,0.0.0.0:12345,ssh,Access SSH Server" "127.0.0.2:1212,0.0.0.0:12346,http,Http server"

### RPX
- On the client side
  - python -m http.server 1212
- On the server side
  - curl -v -H 'Host: tratra' http://127.0.0.1:9996/hello/world
  - this hits the port 1212 on the client side

### WPX
- On the client side
  - python -m http.server 1212
- On the server side
  - Access `http://127.0.0.1:9997/12346/ to access the http server at 127.0.0.1:1212 on the client side

### Port forwarding to client
- On the server side
  - ssh -p 12345 127.0.0.1
  - this should establish connection to 127.0.0.2:22 on the client side

### SSH Terminal
- On the server side
  - Access `http://127.0.0.1:9998/_ssh/[server-id]/[conn-id]/xterm.html` to have the terminal connecting to 127.0.0.2:22


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
curl -X POST --data-binary @client-route.json http://127.0.0.1:1107/_ctl/client-conns/1/routes
```
