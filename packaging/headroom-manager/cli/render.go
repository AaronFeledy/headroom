package cli

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
)

type warningState struct {
	level  int
	window string
}

func render(providers []Provider, now time.Time, color bool, warnings map[string]warningState) string {
	var output strings.Builder
	active := make(map[string]bool)
	fmt.Fprintf(&output, "Headroom  %s\n", now.Local().Format("15:04:05"))
	if len(providers) == 0 {
		clear(warnings)
		output.WriteString("No enabled providers.\n")
		return output.String()
	}
	for _, provider := range providers {
		name := provider.ProviderName
		if strings.EqualFold(name, "Codex") {
			name = "ChatGPT"
		}
		fmt.Fprintf(&output, "\n%s", terminalText(name))
		if provider.Subtitle != nil && strings.TrimSpace(*provider.Subtitle) != "" {
			fmt.Fprintf(&output, "  %s", terminalText(*provider.Subtitle))
		}
		output.WriteByte('\n')
		if provider.Error != nil {
			// An unavailable provider has not recovered or removed its meters.
			prefix := strings.ToLower(provider.ProviderName) + "\x00"
			for key := range warnings {
				if strings.HasPrefix(key, prefix) {
					active[key] = true
				}
			}
			fmt.Fprintf(&output, "  Error: %s\n", terminalText(*provider.Error))
			if provider.NeedsReauth {
				output.WriteString("  Sign-in required\n")
			}
			continue
		}
		if len(provider.Buckets) == 0 {
			output.WriteString("  No usage meters.\n")
			continue
		}
		for _, bucket := range provider.Buckets {
			pace := pacing(provider.ProviderName, bucket, now.UTC())
			key := strings.ToLower(provider.ProviderName) + "\x00" + bucket.ID
			window := ""
			if bucket.ResetsAt != nil {
				window = bucket.ResetsAt.UTC().Format(time.RFC3339Nano)
			}
			previous, initialized := warnings[key]
			if previous.window != window {
				previous.level = 0
			}
			severity := previous.level
			// Keep the last assessment while a known window awaits replacement.
			// Percentage-only fallback on an expired reading creates false alerts.
			expired := initialized && previous.window == window && bucket.ResetsAt != nil && !bucket.ResetsAt.After(now)
			if !expired {
				severity = warningLevel(bucket.Utilization, pace, previous.level)
			}
			warnings[key] = warningState{level: severity, window: window}
			active[key] = true
			level := colored(warningNames[severity], severity, color)
			filled := int(math.Round(bucket.Utilization / 5))
			if filled < 0 {
				filled = 0
			}
			if filled > 20 {
				filled = 20
			}
			fmt.Fprintf(&output, "  %-18s [%s%s] %5.1f%% used  %s\n", terminalText(bucket.Label),
				colored(strings.Repeat("#", filled), severity, color), strings.Repeat("-", 20-filled), bucket.Utilization, level)
			if pace.available {
				position := min(19, max(0, int(math.Round(pace.expected/5))))
				ruler := []rune(strings.Repeat(" ", 20))
				for elapsed := pace.step; elapsed > 0 && elapsed < pace.duration; elapsed += pace.step {
					tick := min(19, int(math.Round(20*float64(elapsed)/float64(pace.duration))))
					ruler[tick] = '·'
				}
				ruler[position] = '^'
				fmt.Fprintf(&output, "  %18s  %s  %s\n", "", string(ruler), pace.label)
			}
			parts := []string{fmt.Sprintf("%.1f%% left", 100-bucket.Utilization), countdown(bucket.ResetsAt, now)}
			if bucket.StatusText != nil && strings.TrimSpace(*bucket.StatusText) != "" {
				parts = append(parts, terminalText(*bucket.StatusText))
			}
			fmt.Fprintf(&output, "  %18s   %s\n", "", strings.Join(parts, " · "))
		}
		if credits := provider.RateLimitResetCredits; credits != nil && credits.AvailableCount > 0 {
			fmt.Fprintf(&output, "  %d banked %s\n", credits.AvailableCount, func() string {
				if credits.AvailableCount == 1 {
					return "reset"
				}
				return "resets"
			}())
		}
	}
	for key := range warnings {
		if !active[key] {
			delete(warnings, key)
		}
	}
	return output.String()
}

func countdown(reset *time.Time, now time.Time) string {
	if reset == nil {
		return "No scheduled reset"
	}
	seconds := int64(math.Ceil(reset.Sub(now.UTC()).Seconds()))
	if seconds <= 0 {
		return "Reset pending"
	}
	minutes := (seconds + 59) / 60
	if minutes >= 1440 {
		return fmt.Sprintf("Resets in %dd %dh", minutes/1440, minutes%1440/60)
	}
	if minutes >= 60 {
		return fmt.Sprintf("Resets in %dh %dm", minutes/60, minutes%60)
	}
	return fmt.Sprintf("Resets in %dm", minutes)
}

func colored(value string, severity int, enabled bool) string {
	if !enabled {
		return value
	}
	colors := []string{"\x1b[38;2;189;147;249m", "\x1b[38;2;241;250;140m", "\x1b[38;2;255;184;108m", "\x1b[38;2;255;85;85m"}
	return colors[severity] + value + "\x1b[0m"
}

// Provider strings are untrusted. Never allow them to issue terminal commands,
// rewrite earlier lines, or hide text with bidirectional/format controls.
func terminalText(value string) string {
	var output strings.Builder
	count, whitespace := 0, false
	for _, character := range value {
		if unicode.IsSpace(character) {
			whitespace = output.Len() > 0
			continue
		}
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf) {
			continue
		}
		if count >= 120 {
			output.WriteRune('…')
			break
		}
		if whitespace {
			output.WriteByte(' ')
			whitespace = false
		}
		output.WriteRune(character)
		count++
	}
	return output.String()
}
