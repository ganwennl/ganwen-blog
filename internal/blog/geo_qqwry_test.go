package blog

import (
	"encoding/binary"
	"net/netip"
	"os"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
)

func geoTestQQWry(t *testing.T, location, area string) []byte {
	t.Helper()
	country, e := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(location))
	if e != nil {
		t.Fatal(e)
	}
	network, e := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(area))
	if e != nil {
		t.Fatal(e)
	}
	raw := make([]byte, 12)
	binary.LittleEndian.PutUint32(raw[8:], 0xffffffff)
	raw = append(raw, country...)
	raw = append(raw, 0)
	raw = append(raw, network...)
	raw = append(raw, 0)
	first := uint32(len(raw))
	binary.LittleEndian.PutUint32(raw[:4], first)
	binary.LittleEndian.PutUint32(raw[4:8], first)
	raw = append(raw, 0, 0, 0, 0, 8, 0, 0)
	return raw
}

func TestQQWryBoundsRedirectAndGBK(t *testing.T) {
	raw := geoTestQQWry(t, "江苏省南京市", "中国电信")
	q, err := ParseQQWry(raw)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := q.Lookup(netip.MustParseAddr("114.114.114.114"))
	if !ok || result.Location != "江苏省南京市" || result.Area != "中国电信" {
		t.Fatalf("lookup: %+v %v", result, ok)
	}
	raw[12] = 0 // immutable copy
	if result, ok = q.Lookup(netip.MustParseAddr("255.255.255.255")); !ok || result.Location != "江苏省南京市" {
		t.Fatal("input mutation changed parsed DB")
	}
	if _, ok = q.Lookup(netip.MustParseAddr("2001:db8::1")); ok {
		t.Fatal("IPv6 accepted by IPv4 DB")
	}
	for _, bad := range []string{"header", "pointer", "cycle", "unterminated", "invalid GBK", "control"} {
		t.Run(bad, func(t *testing.T) {
			raw := geoTestQQWry(t, "荷兰", "Vultr")
			first := binary.LittleEndian.Uint32(raw[:4])
			switch bad {
			case "header":
				binary.LittleEndian.PutUint32(raw[:4], 0xffffffff)
			case "pointer":
				raw[first+4] = 255
				raw[first+5] = 255
				raw[first+6] = 255
			case "cycle":
				copy(raw[12:16], []byte{1, 12, 0, 0})
			case "unterminated":
				for i := 12; i < int(first); i++ {
					raw[i] = 'a'
				}
			case "invalid GBK":
				raw[12] = 0xff
			case "control":
				raw[12] = 10
			}
			if _, err := ParseQQWry(raw); err == nil {
				t.Fatal("corrupt DB accepted")
			}
		})
	}
	for location, expected := range map[string]string{"江苏省南京市": "CN", "中国香港": "HK", "中国台湾": "TW", "荷兰阿姆斯特丹": "NL", "美国加利福尼亚州": "US", "保留地址": "unknown"} {
		if actual := geoCountry(location); actual != expected {
			t.Fatalf("country %s=%s", location, actual)
		}
	}
}

func TestQQWryRedirectModes(t *testing.T) {
	for _, mode := range []string{"whole record to mode2", "country mode2", "zero area"} {
		t.Run(mode, func(t *testing.T) {
			raw := make([]byte, 12)
			binary.LittleEndian.PutUint32(raw[8:], 0xffffffff)
			raw = append(raw, 1, 16, 0, 0, 2, 24, 0, 0, 2, 29, 0, 0)
			country, _ := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("荷兰"))
			area, _ := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("网络"))
			raw = append(raw, country...)
			raw = append(raw, 0)
			raw = append(raw, area...)
			raw = append(raw, 0)
			if mode == "country mode2" {
				copy(raw[12:16], []byte{2, 24, 0, 0})
				copy(raw[16:20], []byte{2, 29, 0, 0})
			}
			if mode == "zero area" {
				copy(raw[20:24], []byte{2, 0, 0, 0})
			}
			first := uint32(len(raw))
			binary.LittleEndian.PutUint32(raw[:4], first)
			binary.LittleEndian.PutUint32(raw[4:8], first)
			raw = append(raw, 0, 0, 0, 0, 8, 0, 0)
			q, err := ParseQQWry(raw)
			if err != nil {
				t.Fatal(err)
			}
			place, ok := q.Lookup(netip.MustParseAddr("1.1.1.1"))
			wantArea := "网络"
			if mode == "zero area" {
				wantArea = ""
			}
			if !ok || place.Location != "荷兰" || place.Area != wantArea {
				t.Fatal(mode, place, ok)
			}
		})
	}
}

// Optional local public fixture: never creates an App, opens blog.db, or records
// an event. The 27 MB upstream DB is deliberately excluded from source control.
func TestQQWryRealLocalDatabase(t *testing.T) {
	raw, err := os.ReadFile("../../data/geoip/qqwry.dat")
	if os.IsNotExist(err) {
		t.Skip("local public QQWry file not installed")
	}
	if err != nil {
		t.Fatal(err)
	}
	q, err := ParseQQWry(raw)
	if err != nil {
		t.Fatal(err)
	}
	if q.Records() < 100000 {
		t.Fatal("unexpected real database record count")
	}
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "114.114.114.114", "66.42.81.91"} {
		result, ok := q.Lookup(netip.MustParseAddr(ip))
		if !ok || result.Location == "" {
			t.Fatalf("lookup %s failed", ip)
		}
		t.Logf("%s: %s / %s", ip, result.Location, result.Area)
	}
}
