package cli

import "encoding/json"

func filterWeeklyJSON(body []byte) ([]byte, error) {
	var providers []map[string]json.RawMessage
	if err := json.Unmarshal(body, &providers); err != nil {
		return nil, err
	}
	for _, provider := range providers {
		var name string
		if raw := provider["provider_name"]; raw != nil {
			if err := json.Unmarshal(raw, &name); err != nil {
				return nil, err
			}
		}
		var buckets []json.RawMessage
		if providerType := decodeProviderType(provider["provider_type"]); providerType != "" {
			name = providerType
		}
		if raw, ok := provider["buckets"]; ok {
			if err := json.Unmarshal(raw, &buckets); err != nil {
				return nil, err
			}
			filtered := make([]json.RawMessage, 0, len(buckets))
			for _, bucket := range buckets {
				var identity Bucket
				if err := json.Unmarshal(bucket, &identity); err != nil {
					return nil, err
				}
				if weeklyBucket(name, identity) {
					filtered = append(filtered, bucket)
				}
			}
			provider["buckets"], _ = json.Marshal(filtered)
		}
	}
	return json.Marshal(providers)
}
