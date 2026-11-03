# Captured Kakao traffic

The `.hex` files here are response bodies recorded from the bridge's **own registered mobile device**,
kept because they pin the field names the parsers depend on: `c/a/l` in `ChatOnRoom`, `chatInfo` in
`CHATINFO`, `members/nickName` in `GETMEM`, `chatLogs/eof` in `MCHATLOGS`. A parser that reads the
wrong key would otherwise only show up as rooms with no members.

They are **not published** with the bridge: they are traffic from a personal account, so they contain
real names, chat ids, profile-image keys and message text. The tests that use them skip when the files
are absent, and `make test` stays green without them.

To record your own, with the tokens of a device you registered (see `docs/configuration.md` for the
`network.tokens_file` bootstrap):

```
go run ./cmd/locoprobe -tokens <tokens.json> -- \
    'CHATINFO chatId:i64=<chat id>' 'GETMEM chatId:i64=<chat id>'
# each line it prints ends with hex=<body>; take that hex into a file named like the verb:
... | sed -n 's/.*hex=//p' > loco/testdata/getmem_direct.hex
```

The names the tests look for are: `chatonroom_direct.hex`, `chatinfo_direct.hex`, `getmem_direct.hex`,
`mchatlogs_direct.hex`, `mchatlogs_memo*.hex`, `decunread_push.hex`, `write_response.hex`,
`kakao_animated_emot_head.hex`. Same verb, same shape — any account works.

One caveat when you re-record: those tests assert the **ids of the capture taken during development**
(`roominfo_test.go`'s `testChatID`/`testSelfID`/`testOtherID`, `chatdata_test.go`'s MemoChat id, and a
`measuredWatermark`). A capture from a different account proves the parsers read the right keys once you
update those constants; the assertions are that specific because they pin what a real gateway sent, not a
shape invented here.
