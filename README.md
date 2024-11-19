
hodu client <servee> <peer1> [<peer2> ...]

    client requests server that it grants access to the list of peers
    reserver


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

## create a server
```
curl -X POST --data-binary @server.json http://127.0.0.1:7777/servers
```
