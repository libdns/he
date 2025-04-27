package he

import (
	"context"
	"log"
	"net/netip"
	"os"
	"testing"

	"github.com/libdns/libdns"
)

var (
	apiKey = os.Getenv("LIBDNS_HE_KEY")
	zone   = os.Getenv("LIBDNS_HE_ZONE")
)

var (
	provider Provider
)

func init() {
	if apiKey == "" {
		log.Fatalf("API key needs to be provided in env var LIBDNS_HE_KEY")
	}
	if zone == "" {
		log.Fatalf("DNS zone needs to be provided in env var LIBDNS_HE_ZONE")
	}
	if zone[len(zone)-1:] != "." {
		// Zone names come from caddy with trailing period
		zone += "."
	}
	provider = Provider{APIKey: apiKey}
}

func TestAppendRecords(t *testing.T) {
	ctx := context.Background()

	records := []libdns.Record{
		libdns.Address{
			Name: "test001",
			IP:   netip.MustParseAddr("192.0.2.1"),
		},
		libdns.Address{
			Name: "test001",
			IP:   netip.MustParseAddr("2001:0db8:2::1"),
		},
		libdns.TXT{
			Name: "test001",
			Text: "ZYXWVUTSRQPONMLKJIHGFEDCBA",
		},
		libdns.RR{
			Name: "test002",
			Type: "TXT",
			Data: "2GXNBB3JYUNAHDSAX2K37GVW2M",
		},
	}

	createdRecords, err := provider.AppendRecords(ctx, zone, records)
	if err != nil {
		t.Errorf("%v", err)
	}

	if len(records) != len(createdRecords) {
		t.Errorf("Number of appended records does not match number of records")
	}
}

func TestGetRecords(t *testing.T) {
	ctx := context.Background()

	records, err := provider.GetRecords(ctx, zone)
	if err != nil {
		t.Errorf("%v", err)
	}

	if len(records) == 0 {
		t.Errorf("No records")
	}
}

func TestSetRecords(t *testing.T) {
	ctx := context.Background()

	goodRecords := []libdns.Record{
		libdns.Address{
			Name: "test001",
			IP:   netip.MustParseAddr("198.51.100.1"),
		},
		libdns.Address{
			Name: "test001",
			IP:   netip.MustParseAddr("2001:0db8::1"),
		},
		libdns.TXT{
			Name: "test001",
			Text: "ABCDEFGHIJKLMNOPQRSTUVWXYZ",
		},
		libdns.Address{
			Name: "test002",
			IP:   netip.MustParseAddr("198.51.100.2"),
		},
		libdns.RR{
			Name: "test002",
			Type: "AAAA",
			Data: "2001:0db8::1",
		},
		libdns.Address{
			Name: "test003",
			IP:   netip.MustParseAddr("198.51.100.3"),
		},
	}

	createdRecords, err := provider.SetRecords(ctx, zone, goodRecords)
	if err != nil {
		t.Fatalf("adding records failed: %v", err)
	}

	if len(goodRecords) != len(createdRecords) {
		t.Fatalf("Number of added records does not match number of records")
	}

	badRecords := []libdns.Record{
		libdns.CNAME{
			Name:   "test000",
			Target: "example.org",
		},
		libdns.RR{
			Name: "test000",
			Type: "SRV",
			Data: "1 2 1234 example.com",
		},
	}

	for _, badRecord := range badRecords {
		_, err = provider.SetRecords(ctx, zone, []libdns.Record{badRecord})
		if err == nil {
			t.Fatalf("unsupported records should return error")
		}
	}
}

func TestDeleteRecords(t *testing.T) {
	ctx := context.Background()

	records := []libdns.Record{
		libdns.RR{
			Type: "A",
			Name: "test001",
		},
		libdns.RR{
			Type: "AAAA",
			Name: "test001",
		},
		libdns.RR{
			Type: "TXT",
			Name: "test001",
		},
		libdns.Address{
			Name: "test002",
			IP:   netip.MustParseAddr("198.51.100.2"),
		},
	}

	deletedRecords, err := provider.DeleteRecords(ctx, zone, records)
	if err != nil {
		t.Errorf("deleting records failed: %v", err)
	}

	if len(records) != len(deletedRecords) {
		t.Errorf("Number of deleted records does not match number of records")
	}
}
