# Captured Kakao traffic

`loginlist_response.bin` is the raw `LOGINLIST` response body of one authenticated LOCO session — the
document the bridge reads its chat list from (`chatDatas`, `lastTokenID`, `revision`). The test that
uses it (`loco/chatdata_test.go`) checks that the chat-list parse keeps working on a real body rather
than one written by hand.

It is **not published**: it contains a personal account's chat ids, names and message text. The test
skips when the file is missing.

To record your own:

```
go run ./cmd/locoauth -tokens <tokens.json> -dump testdata/loginlist_response.bin
```

`loco/chatdata_test.go` asserts the chat ids of the capture taken during development (its MemoChat id is
the account's own self-chat), so a capture from another account needs that constant updated too.
