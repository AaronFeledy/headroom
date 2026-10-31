package grok

import (
	"encoding/binary"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var errResetInventory = errors.New("grok: invalid reset inventory")

func decodeResetInventory(body []byte, headerStatus string, now time.Time) (*usage.ResetInventoryDetails, error) {
	if len(body) > maxWebUsageResponseBytes || headerStatus != "" && headerStatus != "0" {
		return nil, errResetInventory
	}
	var payload []byte
	dataSeen, trailerSeen := false, false
	status := headerStatus
	for len(body) > 0 {
		if len(body) < 5 || trailerSeen {
			return nil, errResetInventory
		}
		flag, size := body[0], uint64(binary.BigEndian.Uint32(body[1:5]))
		body = body[5:]
		if size > uint64(len(body)) {
			return nil, errResetInventory
		}
		frame := body[:int(size)]
		body = body[int(size):]
		switch flag {
		case 0:
			if dataSeen {
				return nil, errResetInventory
			}
			dataSeen, payload = true, frame
		case 0x80:
			trailerSeen = true
			found := false
			for _, line := range strings.Split(string(frame), "\r\n") {
				if line == "" {
					continue
				}
				name, value, ok := strings.Cut(line, ":")
				if !ok {
					return nil, errResetInventory
				}
				if strings.EqualFold(strings.TrimSpace(name), "grpc-status") {
					value = strings.TrimSpace(value)
					if found || value != "0" || status != "" && status != value {
						return nil, errResetInventory
					}
					found, status = true, value
				}
			}
			if !found {
				return nil, errResetInventory
			}
		default:
			return nil, errResetInventory
		}
	}
	if !dataSeen || status != "0" {
		return nil, errResetInventory
	}
	windows := []usage.ResetWindow{}
	count, validCount := int64(0), int64(0)
	classifiable := true
	for len(payload) > 0 {
		num, typ, n := protowire.ConsumeTag(payload)
		if n < 0 {
			return nil, errResetInventory
		}
		payload = payload[n:]
		if num != 10 {
			n = protowire.ConsumeFieldValue(num, typ, payload)
			if n < 0 {
				return nil, errResetInventory
			}
			payload = payload[n:]
			continue
		}
		if typ != protowire.BytesType || count == usage.MaxDetailItems {
			return nil, errResetInventory
		}
		token, n := protowire.ConsumeBytes(payload)
		if n < 0 {
			return nil, errResetInventory
		}
		payload = payload[n:]
		window, err := decodeResetWindow(token)
		if err != nil {
			return nil, err
		}
		count++
		if window.ValidFrom == nil || window.ExpiresAt == nil {
			classifiable = false
		} else if !now.Before(*window.ValidFrom) && now.Before(*window.ExpiresAt) {
			validCount++
		}
		merged := false
		for i, old := range windows {
			if equalTime(old.ValidFrom, window.ValidFrom) && equalTime(old.ExpiresAt, window.ExpiresAt) {
				windows[i].Count++
				merged = true
				break
			}
		}
		if !merged {
			windows = append(windows, window)
		}
	}
	sort.Slice(windows, func(i, j int) bool {
		if !equalTime(windows[i].ValidFrom, windows[j].ValidFrom) {
			return beforeTime(windows[i].ValidFrom, windows[j].ValidFrom)
		}
		return beforeTime(windows[i].ExpiresAt, windows[j].ExpiresAt)
	})
	inventory := &usage.ResetInventoryDetails{Observation: observation(now), ReportedCount: &count, Windows: windows}
	if classifiable {
		inventory.ValidNowCount = &validCount
	}
	return inventory, nil
}

func equalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func beforeTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b != nil
	}
	return a.Before(*b)
}

func decodeResetWindow(body []byte) (usage.ResetWindow, error) {
	window := usage.ResetWindow{Count: 1}
	seen := map[protowire.Number]bool{}
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		if n < 0 {
			return window, errResetInventory
		}
		body = body[n:]
		switch num {
		case 10:
			// The redemption handle is skipped by wire length, never decoded,
			// retained, hashed, or converted to a string.
			if typ != protowire.BytesType {
				return window, errResetInventory
			}
			size, prefix := protowire.ConsumeVarint(body)
			if prefix < 0 || size > uint64(len(body)-prefix) {
				return window, errResetInventory
			}
			body = body[prefix+int(size):]
		case 20, 30:
			if typ != protowire.BytesType || seen[num] {
				return window, errResetInventory
			}
			seen[num] = true
			value, length := protowire.ConsumeBytes(body)
			if length < 0 {
				return window, errResetInventory
			}
			body = body[length:]
			var timestamp timestamppb.Timestamp
			if err := proto.Unmarshal(value, &timestamp); err != nil {
				return window, errResetInventory
			}
			if err := timestamp.CheckValid(); err != nil {
				return window, errResetInventory
			}
			parsed := timestamp.AsTime().UTC()
			if num == 20 {
				window.ValidFrom = &parsed
			} else {
				window.ExpiresAt = &parsed
			}
		default:
			n = protowire.ConsumeFieldValue(num, typ, body)
			if n < 0 {
				return window, errResetInventory
			}
			body = body[n:]
		}
	}
	if window.ValidFrom != nil && window.ExpiresAt != nil && !window.ValidFrom.Before(*window.ExpiresAt) {
		return window, errResetInventory
	}
	return window, nil
}
