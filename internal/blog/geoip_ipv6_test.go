package blog

import (
	"encoding/binary"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type ipv6GeoTestEntry struct {
	high, low, pointer uint64
}

func ipv6GeoTestPut(raw []byte, value uint64) {
	for i := range raw {
		raw[i] = byte(value >> uint(i*8))
	}
}

func ipv6GeoTestHeader() []byte {
	raw := make([]byte, ipv6GeoHeaderBytes)
	copy(raw, "IPDB")
	binary.LittleEndian.PutUint16(raw[4:6], 1)
	binary.LittleEndian.PutUint64(raw[32:40], ipv6GeoHeaderBytes)
	return append(raw, []byte("20261008\x00")...)
}

func ipv6GeoTestAssemble(t *testing.T, data []byte, ipBytes, width int, entries []ipv6GeoTestEntry) []byte {
	t.Helper()
	raw := append([]byte(nil), data...)
	raw[6], raw[7] = byte(width), byte(ipBytes)
	binary.LittleEndian.PutUint64(raw[8:16], uint64(len(entries)))
	binary.LittleEndian.PutUint64(raw[16:24], uint64(len(raw)))
	for _, entry := range entries {
		key := make([]byte, ipBytes+width)
		if ipBytes <= 8 {
			ipv6GeoTestPut(key[:ipBytes], entry.high>>uint(8*(8-ipBytes)))
		} else {
			binary.LittleEndian.PutUint64(key[:8], entry.high)
			ipv6GeoTestPut(key[8:ipBytes], entry.low>>uint(8*(16-ipBytes)))
		}
		ipv6GeoTestPut(key[ipBytes:], entry.pointer)
		raw = append(raw, key...)
	}
	return raw
}

// Shared by manager tests: an entirely synthetic two-record UTF-8 database.
func geoTestIPv6DB(t *testing.T) []byte {
	t.Helper()
	data := ipv6GeoTestHeader()
	google := uint64(len(data))
	data = append(data, []byte("全球\x00Google Inc. 骨干网\x00")...)
	cloudflare := uint64(len(data))
	data = append(data, []byte("全球\x00Cloudflare, Inc. Anycast网段\x00")...)
	return ipv6GeoTestAssemble(t, data, 8, 3, []ipv6GeoTestEntry{{high: 0x2001486000000000, pointer: google}, {high: 0x2606470000000000, pointer: cloudflare}})
}

func TestIPv6GeoDBLookupBoundariesAndImmutability(t *testing.T) {
	raw := geoTestIPv6DB(t)
	db, err := ParseIPv6GeoDB(raw)
	if err != nil {
		t.Fatal(err)
	}
	if db.Records() != 2 || db.Version() != "20261008" {
		t.Fatalf("metadata: %d %q", db.Records(), db.Version())
	}
	for _, address := range []string{"2001:4860::", "2001:4860::8888", "2001:4860::ffff:ffff:ffff:ffff", "2606:46ff:ffff:ffff:ffff:ffff:ffff:ffff", "2001:4860::8888%eth0"} {
		place, ok := db.Lookup(netip.MustParseAddr(address))
		if !ok || place != (QQWryLocation{Location: "全球", Area: "Google Inc. 骨干网"}) {
			t.Fatalf("Google boundary %s: %+v %v", address, place, ok)
		}
	}
	for _, address := range []string{"2606:4700::", "2606:4700::1111", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff"} {
		place, ok := db.Lookup(netip.MustParseAddr(address))
		if !ok || place.Area != "Cloudflare, Inc. Anycast网段" {
			t.Fatalf("Cloudflare boundary %s: %+v %v", address, place, ok)
		}
	}
	for _, address := range []string{"::", "2001:485f:ffff:ffff:ffff:ffff:ffff:ffff", "8.8.8.8", "::ffff:8.8.8.8"} {
		if _, ok := db.Lookup(netip.MustParseAddr(address)); ok {
			t.Fatalf("out-of-range address accepted: %s", address)
		}
	}
	if _, ok := db.Lookup(netip.Addr{}); ok {
		t.Fatal("invalid address accepted")
	}
	for i := range raw {
		raw[i] = 0
	}
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 100; j++ {
				place, ok := db.Lookup(netip.MustParseAddr("2001:4860::8888"))
				if !ok || place.Area != "Google Inc. 骨干网" {
					t.Error("caller mutation changed concurrent lookup")
					return
				}
			}
		}()
	}
	workers.Wait()
	var empty *IPv6GeoDB
	if empty.Records() != 0 || empty.Version() != "" {
		t.Fatal("nil metadata was not safe")
	}
	if _, ok := empty.Lookup(netip.MustParseAddr("::1")); ok {
		t.Fatal("nil database accepted lookup")
	}
}

func TestIPv6GeoDBCompressedIndexWidths(t *testing.T) {
	for _, ipBytes := range []int{1, 2, 4, 8, 9, 12, 16} {
		for _, width := range []int{1, 3, 8} {
			data := ipv6GeoTestHeader()
			first := uint64(len(data))
			data = append(data, []byte("甲\x00First\x00")...)
			second := uint64(len(data))
			data = append(data, []byte("乙\x00Second\x00")...)
			entries := []ipv6GeoTestEntry{{high: 0x2001000000000000, pointer: first}, {high: 0x8001000000000000, pointer: second}}
			if ipBytes > 8 {
				entries[0].low = 0x1100000000000000
				entries[1].high, entries[1].low = entries[0].high, 0x8800000000000000
			}
			db, err := ParseIPv6GeoDB(ipv6GeoTestAssemble(t, data, ipBytes, width, entries))
			if err != nil {
				t.Fatalf("ip width %d pointer width %d: %v", ipBytes, width, err)
			}
			for i, want := range []string{"First", "Second"} {
				var address [16]byte
				binary.BigEndian.PutUint64(address[:8], entries[i].high)
				binary.BigEndian.PutUint64(address[8:], entries[i].low)
				if place, ok := db.Lookup(netip.AddrFrom16(address)); !ok || place.Area != want {
					t.Fatalf("width %d/%d key %d: %+v %v", ipBytes, width, i, place, ok)
				}
			}
			if ipBytes > 8 {
				if _, ok := db.Lookup(netip.MustParseAddr("2001::")); ok {
					t.Fatalf("width %d: low-half lower bound ignored", ipBytes)
				}
			}
		}
	}
}

func TestIPv6GeoDBRedirectsAndEmptyArea(t *testing.T) {
	data := ipv6GeoTestHeader()
	location := uint64(len(data))
	data = append(data, []byte("中国北京市\x00")...)
	area := uint64(len(data))
	data = append(data, []byte("中国移动CMNET网络\x00")...)
	locationRedirect := uint64(len(data))
	data = append(data, 2, 0, 0, 0)
	ipv6GeoTestPut(data[locationRedirect+1:locationRedirect+4], location)
	record := uint64(len(data))
	data = append(data, 2, 0, 0, 0, 2, 0, 0, 0)
	ipv6GeoTestPut(data[record+1:record+4], locationRedirect)
	ipv6GeoTestPut(data[record+5:record+8], area)
	wholeRecordRedirect := uint64(len(data))
	data = append(data, 1, 0, 0, 0)
	ipv6GeoTestPut(data[wholeRecordRedirect+1:wholeRecordRedirect+4], record)
	emptyAreaRecord := uint64(len(data))
	data = append(data, 2, 0, 0, 0, 2, 0, 0, 0)
	ipv6GeoTestPut(data[emptyAreaRecord+1:emptyAreaRecord+4], location)
	raw := ipv6GeoTestAssemble(t, data, 8, 3, []ipv6GeoTestEntry{{pointer: wholeRecordRedirect}, {high: 0x8000000000000000, pointer: emptyAreaRecord}})
	db, err := ParseIPv6GeoDB(raw)
	if err != nil {
		t.Fatal(err)
	}
	if place, ok := db.Lookup(netip.MustParseAddr("2409:8900::1")); !ok || place != (QQWryLocation{"中国北京市", "中国移动CMNET网络"}) {
		t.Fatalf("nested redirects: %+v %v", place, ok)
	}
	if place, ok := db.Lookup(netip.MustParseAddr("8000::1")); !ok || place != (QQWryLocation{"中国北京市", ""}) {
		t.Fatalf("zero-pointer empty area: %+v %v", place, ok)
	}
	data = ipv6GeoTestHeader()
	record = uint64(len(data))
	data = append(data, 0, 0)
	if _, err := ParseIPv6GeoDB(ipv6GeoTestAssemble(t, data, 8, 3, []ipv6GeoTestEntry{{pointer: record}})); err != nil {
		t.Fatalf("two literal empty strings rejected: %v", err)
	}
}

func TestIPv6GeoDBRejectsCorruption(t *testing.T) {
	mutations := map[string]func([]byte) []byte{
		"short header":       func(b []byte) []byte { return b[:39] },
		"magic":              func(b []byte) []byte { b[0] = 'X'; return b },
		"version":            func(b []byte) []byte { b[4] = 2; return b },
		"pointer width zero": func(b []byte) []byte { b[6] = 0; return b },
		"pointer width over": func(b []byte) []byte { b[6] = 9; return b },
		"IP width zero":      func(b []byte) []byte { b[7] = 0; return b },
		"IP width over":      func(b []byte) []byte { b[7] = 17; return b },
		"zero count":         func(b []byte) []byte { binary.LittleEndian.PutUint64(b[8:16], 0); return b },
		"count overflow":     func(b []byte) []byte { binary.LittleEndian.PutUint64(b[8:16], ^uint64(0)); return b },
		"header index":       func(b []byte) []byte { binary.LittleEndian.PutUint64(b[16:24], 39); return b },
		"index overflow":     func(b []byte) []byte { binary.LittleEndian.PutUint64(b[16:24], ^uint64(0)); return b },
		"truncated index":    func(b []byte) []byte { return b[:len(b)-1] },
		"duplicate index": func(b []byte) []byte {
			first := int(binary.LittleEndian.Uint64(b[16:24]))
			copy(b[first+11:first+19], b[first:first+8])
			return b
		},
		"unsigned unsorted index": func(b []byte) []byte {
			first := int(binary.LittleEndian.Uint64(b[16:24]))
			binary.LittleEndian.PutUint64(b[first:first+8], ^uint64(0))
			return b
		},
		"later record invalid": func(b []byte) []byte {
			first := int(binary.LittleEndian.Uint64(b[16:24]))
			ipv6GeoTestPut(b[first+19:first+22], uint64(first))
			return b
		},
		"header record pointer": func(b []byte) []byte {
			first := int(binary.LittleEndian.Uint64(b[16:24]))
			ipv6GeoTestPut(b[first+8:first+11], 7)
			return b
		},
		"zero record pointer": func(b []byte) []byte {
			first := int(binary.LittleEndian.Uint64(b[16:24]))
			clear(b[first+8 : first+11])
			return b
		},
		"edition pointer overflow": func(b []byte) []byte { binary.LittleEndian.PutUint64(b[32:40], ^uint64(0)); return b },
		"invalid edition date":     func(b []byte) []byte { copy(b[40:48], "20260230"); return b },
		"invalid UTF8":             func(b []byte) []byte { b[49] = 0xff; return b },
		"control character":        func(b []byte) []byte { b[56] = '\n'; return b },
		"unterminated record": func(b []byte) []byte {
			first := int(binary.LittleEndian.Uint64(b[16:24]))
			for i := 49; i < first; i++ {
				b[i] = 'x'
			}
			return b
		},
		"record redirect cycle": func(b []byte) []byte { b[49] = 1; ipv6GeoTestPut(b[50:53], 49); return b },
		"string redirect cycle": func(b []byte) []byte { b[49] = 2; ipv6GeoTestPut(b[50:53], 49); return b },
		"redirect to header":    func(b []byte) []byte { b[49] = 2; ipv6GeoTestPut(b[50:53], 1); return b },
		"record redirect to zero": func(b []byte) []byte {
			b[49] = 1
			clear(b[50:53])
			return b
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseIPv6GeoDB(mutate(geoTestIPv6DB(t))); err == nil {
				t.Fatal("corrupt database accepted")
			}
		})
	}
	for _, data := range [][]byte{append(ipv6GeoTestHeader(), 1), append(ipv6GeoTestHeader(), 2), append(ipv6GeoTestHeader(), []byte("Country\x00")...), append(ipv6GeoTestHeader(), []byte(strings.Repeat("a", ipv6GeoMaxTextBytes+1)+"\x00Area\x00")...)} {
		if _, err := ParseIPv6GeoDB(ipv6GeoTestAssemble(t, data, 8, 3, []ipv6GeoTestEntry{{pointer: 49}})); err == nil {
			t.Fatal("short pointer, missing area, or oversized text accepted")
		}
	}
	data := append(ipv6GeoTestHeader(), []byte(strings.Repeat("中", ipv6GeoMaxTextRunes+1)+"\x00Area\x00")...)
	if _, err := ParseIPv6GeoDB(ipv6GeoTestAssemble(t, data, 8, 3, []ipv6GeoTestEntry{{pointer: 49}})); err == nil {
		t.Fatal("oversized Unicode label accepted")
	}
	data = append(ipv6GeoTestHeader(), []byte(strings.Repeat("中", ipv6GeoMaxTextRunes)+"\x00Area\x00")...)
	if _, err := ParseIPv6GeoDB(ipv6GeoTestAssemble(t, data, 8, 3, []ipv6GeoTestEntry{{pointer: 49}})); err != nil {
		t.Fatalf("maximum-length complete Unicode label rejected: %v", err)
	}
}

func TestIPv6GeoDBRedirectDepthAndCrossCycles(t *testing.T) {
	for _, flag := range []byte{1, 2} {
		data := ipv6GeoTestHeader()
		start := uint64(len(data))
		for i := 0; i < ipv6GeoMaxRedirects+1; i++ {
			offset := uint64(len(data))
			data = append(data, flag, 0, 0, 0)
			ipv6GeoTestPut(data[offset+1:offset+4], offset+4)
		}
		data = append(data, []byte("Country\x00Area\x00")...)
		if _, err := ParseIPv6GeoDB(ipv6GeoTestAssemble(t, data, 8, 3, []ipv6GeoTestEntry{{pointer: start}})); err == nil {
			t.Fatalf("unbounded redirect chain accepted: flag %d", flag)
		}
		data = ipv6GeoTestHeader()
		data = append(data, flag, 53, 0, 0, flag, 49, 0, 0, 0)
		if _, err := ParseIPv6GeoDB(ipv6GeoTestAssemble(t, data, 8, 3, []ipv6GeoTestEntry{{pointer: 49}})); err == nil {
			t.Fatalf("two-node redirect cycle accepted: flag %d", flag)
		}
	}
}

func TestLoadIPv6GeoDBFiles(t *testing.T) {
	directory := t.TempDir()
	if _, err := LoadIPv6GeoDB(filepath.Join(directory, "missing.db")); err == nil {
		t.Fatal("missing database accepted")
	}
	if _, err := LoadIPv6GeoDB(directory); err == nil {
		t.Fatal("directory accepted")
	}
	path := filepath.Join(directory, "geo.db")
	if err := os.WriteFile(path, geoTestIPv6DB(t), 0600); err != nil {
		t.Fatal(err)
	}
	db, err := LoadIPv6GeoDB(path)
	if err != nil || db.Records() != 2 {
		t.Fatalf("valid file: %+v %v", db, err)
	}
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIPv6GeoDB(path); err == nil {
		t.Fatal("corrupt file accepted")
	}
	if place, ok := db.Lookup(netip.MustParseAddr("2001:4860::8888")); !ok || place.Area != "Google Inc. 骨干网" {
		t.Fatal("replacement affected loaded database")
	}
}

func TestIPv6GeoDBBundledRealDatabase(t *testing.T) {
	path := filepath.Join("..", "..", "data", "geoip", "ipv6wry.db")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("optional bundled IPv6Wry database is not present in this checkout")
	}
	db, err := LoadIPv6GeoDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if db.Records() < 100000 || len(db.Version()) != 8 {
		t.Fatalf("unexpected real database: %d records, version %q", db.Records(), db.Version())
	}
	t.Logf("validated all %d records; internal edition %s", db.Records(), db.Version())
	cases := map[string]QQWryLocation{
		"2001:4860::8888":                   {"全球", "Google Inc. 骨干网"},
		"2606:4700::1111":                   {"全球", "Cloudflare, Inc. Anycast网段"},
		"2409:8900::1":                      {"中国北京市", "中国移动CMNET网络"},
		"2409:8900:0:0:ffff:ffff:ffff:ffff": {"中国北京市", "中国移动CMNET网络"},
		"2002:0808:0808::1":                 {"IANA特殊地址", "6to4隧道地址"},
		"::":                                {"IANA保留地址", ""},
	}
	for address, want := range cases {
		if place, ok := db.Lookup(netip.MustParseAddr(address)); !ok || place != want {
			t.Fatalf("real %s: %+v %v; want %+v", address, place, ok, want)
		}
	}
	if place, ok := db.Lookup(netip.MustParseAddr("ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff")); !ok || place.Location != "ZX公网IPv6库" || place.Area != db.Version()+"版" {
		t.Fatalf("real edition sentinel: %+v %v", place, ok)
	}
}

func FuzzIPv6GeoDB(f *testing.F) {
	data := ipv6GeoTestHeader()
	data = append(data, []byte("Country\x00Area\x00")...)
	data[6], data[7] = 3, 8
	binary.LittleEndian.PutUint64(data[8:16], 1)
	binary.LittleEndian.PutUint64(data[16:24], uint64(len(data)))
	data = append(data, 0, 0, 0, 0, 0, 0, 0, 0, 49, 0, 0)
	f.Add(data)
	f.Add([]byte("IPDB"))
	f.Fuzz(func(t *testing.T, raw []byte) {
		db, err := ParseIPv6GeoDB(raw)
		if err == nil {
			db.Lookup(netip.MustParseAddr("2001:db8::1"))
			db.Lookup(netip.MustParseAddr("ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff"))
		}
	})
}
