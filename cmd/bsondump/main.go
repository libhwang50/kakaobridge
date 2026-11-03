// SPDX-License-Identifier: AGPL-3.0-or-later

// Command bsondump prints the BSON elements of a LOCO body, in wire order.
//
//	bsondump -hex 18080000…                 # one hex string
//	bsondump -harvest harvest.json -field checkin_body   # a captured body from the harvest file
//
// Field order matters for some LOCO calls (MCHATLOGS needs type 0x04, not 0x09), so this tool
// reports order and BSON types verbatim; use it when adding a body builder to loco/.
package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/libhwang50/kakaobridge/loco"
)

func dump(elems []loco.Elem, depth int) {
	for _, e := range elems {
		pad := strings.Repeat("  ", depth+1)
		switch e.Type {
		case 0x03, 0x04, 0x09:
			fmt.Printf("%s%-14s type=0x%02x {\n", pad, e.Key, e.Type)
			if nested, ok := e.Val.([]loco.Elem); ok {
				dump(nested, depth+1)
			}
			fmt.Printf("%s}\n", pad)
		default:
			fmt.Printf("%s%-14s type=0x%02x %v\n", pad, e.Key, e.Type, e.Val)
		}
	}
}

func main() {
	hexs := flag.String("hex", "", "BSON body as hex")
	harv := flag.String("harvest", "", "harvest json to read a body from")
	field := flag.String("field", "checkin_body", "which field of the harvest json (default checkin_body)")
	flag.Parse()

	bodyHex := *hexs
	if bodyHex == "" && *harv != "" {
		var h map[string]any
		b, err := os.ReadFile(*harv)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := json.Unmarshal(b, &h); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		v, ok := h[*field].(string)
		if !ok {
			fmt.Fprintf(os.Stderr, "field %q missing; available:", *field)
			for k := range h {
				fmt.Fprintf(os.Stderr, " %s", k)
			}
			fmt.Fprintln(os.Stderr)
			os.Exit(1)
		}
		bodyHex = v
	}
	if bodyHex == "" {
		fmt.Fprintln(os.Stderr, "nothing to dump: pass -hex or -harvest")
		os.Exit(1)
	}
	hexBytes, err := hex.DecodeString(bodyHex)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hex:", err)
		os.Exit(1)
	}
	elems, _, derr := loco.DecodeDoc(hexBytes, 0)
	if derr != nil {
		fmt.Fprintln(os.Stderr, "decode:", derr)
		os.Exit(1)
	}
	fmt.Printf("%s (%d bytes, %d top-level fields)\n", *field, len(hexBytes), len(elems))
	dump(elems, 0)
}
