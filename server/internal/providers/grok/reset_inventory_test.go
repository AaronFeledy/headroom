package grok

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func inventoryFrame(flag byte, payload []byte) []byte {
	frame := make([]byte, 5)
	frame[0] = flag
	binary.BigEndian.PutUint32(frame[1:], uint32(len(payload)))
	return append(frame, payload...)
}

func inventoryReply(payload []byte) []byte {
	return append(inventoryFrame(0, payload), inventoryFrame(0x80, []byte("grpc-status: 0\r\n"))...)
}

func inventoryToken(start, end *timestamppb.Timestamp) []byte {
	body := protowire.AppendTag(nil, 10, protowire.BytesType)
	body = protowire.AppendBytes(body, []byte{0xff, 0xfe, 's', 'e', 'c', 'r', 'e', 't'})
	for i, stamp := range []*timestamppb.Timestamp{start, end} {
		if stamp == nil {
			continue
		}
		encoded, err := proto.Marshal(stamp)
		if err != nil {
			panic(err)
		}
		body = protowire.AppendTag(body, protowire.Number(20+i*10), protowire.BytesType)
		body = protowire.AppendBytes(body, encoded)
	}
	return protowire.AppendBytes(protowire.AppendTag(nil, 10, protowire.BytesType), body)
}

func TestInventoryDecoderDistinguishesEmptySuccessFromUnknown(t *testing.T) {
	now := time.Unix(100, 0)
	inventory, err := decodeResetInventory(inventoryReply(nil), "", now)
	if err != nil || inventory == nil || inventory.ReportedCount == nil || *inventory.ReportedCount != 0 || inventory.ValidNowCount == nil || *inventory.ValidNowCount != 0 || inventory.Windows == nil || len(inventory.Windows) != 0 {
		t.Fatalf("empty success: %#v %v", inventory, err)
	}
	for _, body := range [][]byte{nil, inventoryFrame(0, nil), inventoryFrame(0x80, []byte("grpc-status: 0\r\n")), inventoryFrame(0, []byte{0xff}), append(inventoryFrame(0, nil), inventoryFrame(0x80, []byte("grpc-status: 7\r\n"))...), append(inventoryReply(nil), 1), inventoryFrame(1, nil), {0, 0, 0, 0, 9}, append(inventoryFrame(0, nil), inventoryFrame(0x80, []byte("grpc-status: 0\r\ngrpc-status: 0\r\n"))...)} {
		if got, err := decodeResetInventory(body, "", now); err == nil || got != nil {
			t.Fatal("malformed inventory accepted")
		}
	}
	if got, err := decodeResetInventory(inventoryReply(nil), "16", now); err == nil || got != nil {
		t.Fatal("failed header accepted")
	}
}

func TestInventoryDecoderGroupsAnonymousWindowsAndClassifiesBounds(t *testing.T) {
	now := time.Unix(100, 0)
	start, end := timestamppb.New(now.Add(-time.Hour)), timestamppb.New(now.Add(time.Hour))
	payload := append(inventoryToken(start, end), inventoryToken(start, end)...)
	payload = append(payload, inventoryToken(timestamppb.New(now.Add(time.Hour)), timestamppb.New(now.Add(2*time.Hour)))...)
	payload = append(payload, inventoryToken(timestamppb.New(now.Add(-2*time.Hour)), timestamppb.New(now))...)
	inventory, err := decodeResetInventory(inventoryReply(payload), "", now)
	if err != nil {
		t.Fatal(err)
	}
	if *inventory.ReportedCount != 4 || *inventory.ValidNowCount != 2 || len(inventory.Windows) != 3 || inventory.Windows[1].Count != 2 {
		t.Fatalf("inventory: %#v", inventory)
	}
	encoded, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") {
		t.Fatal("spend handle retained")
	}
}

func TestInventoryDecoderRejectsPartialExactCounts(t *testing.T) {
	now := time.Unix(100, 0)
	valid := timestamppb.New(now)
	missing := inventoryToken(nil, timestamppb.New(now.Add(time.Hour)))
	inventory, err := decodeResetInventory(inventoryReply(missing), "", now)
	if err != nil || inventory.ValidNowCount != nil || *inventory.ReportedCount != 1 {
		t.Fatal("missing bound became permission")
	}
	oversized := []byte{}
	for range 65 {
		oversized = append(oversized, missing...)
	}
	for _, body := range [][]byte{oversized, inventoryToken(valid, valid), inventoryToken(&timestamppb.Timestamp{Seconds: 1, Nanos: -1}, valid), inventoryToken(&timestamppb.Timestamp{Seconds: 253402300800}, valid)} {
		if got, err := decodeResetInventory(inventoryReply(body), "", now); err == nil || got != nil {
			t.Fatal("partial inventory accepted")
		}
	}
}

func TestInventoryRequestConstructionIsReadOnly(t *testing.T) {
	req, err := resetInventoryRequest(context.Background(), resetInventoryURL, "synthetic-token")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	if err := req.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if req.Method != "POST" || req.URL.String() != resetInventoryURL || string(body) != string([]byte{0, 0, 0, 0, 0}) || req.Header.Get("Authorization") != "Bearer synthetic-token" || req.Header.Get("Content-Type") != "application/grpc-web+proto" || req.Header.Get("X-Grpc-Web") != "1" || req.Header.Get("Origin") != "https://grok.com" {
		t.Fatal("incorrect inventory read request")
	}
}
