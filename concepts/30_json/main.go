package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Time is a custom time type that formats as "2006-01-02 15:04:05"
type Time struct {
	time.Time
}

const customTimeLayout = "2006-01-02 15:04:05"

// MarshalJSON implements the json.Marshaler interface
func (t Time) MarshalJSON() ([]byte, error) {
	if t.Time.IsZero() {
		return []byte("null"), nil
	}
	formatted := t.Time.Format(customTimeLayout)
	return []byte(fmt.Sprintf(`"%s"`, formatted)), nil
}

// UnmarshalJSON implements the json.Unmarshaler interface
func (t *Time) UnmarshalJSON(data []byte) error {
	// Handle null values
	if string(data) == "null" {
		t.Time = time.Time{}
		return nil
	}

	// Remove quotes from the JSON string
	str := string(data)
	str = strings.Trim(str, `"`)

	// Parse the time string
	parsed, err := time.Parse(customTimeLayout, str)
	if err != nil {
		return fmt.Errorf("failed to parse time %q: %v", str, err)
	}
	t.Time = parsed
	return nil
}

// String implements the Stringer interface for better display
func (t Time) String() string {
	if t.Time.IsZero() {
		return "null"
	}
	return t.Time.Format(customTimeLayout)
}

// Event represents an event with custom time fields
type Event struct {
	Name      string `json:"name"`
	StartTime Time   `json:"start_time"`
	EndTime   Time   `json:"end_time"`
	CreatedAt Time   `json:"created_at"`
}

func main() {
	// Demonstrate custom marshaling/unmarshaling
	fmt.Println("=== Custom Time Marshal/Unmarshal Demo ===")

	// Create an event with custom times
	now := time.Now()
	start := Time{Time: now}
	end := Time{Time: now.Add(2 * time.Hour)}
	created := Time{Time: now.Add(-1 * time.Hour)}

	event := Event{
		Name:      "Go Conference 2026",
		StartTime: start,
		EndTime:   end,
		CreatedAt: created,
	}

	// Marshal to JSON
	jsonData, err := json.MarshalIndent(event, "", "  ")
	if err != nil {
		fmt.Printf("Error marshaling: %v\n", err)
		return
	}
	fmt.Println("\nMarshaled JSON:")
	fmt.Println(string(jsonData))

	// Unmarshal from JSON back to Event
	var newEvent Event
	err = json.Unmarshal(jsonData, &newEvent)
	if err != nil {
		fmt.Printf("Error unmarshaling: %v\n", err)
		return
	}
	fmt.Println("\nUnmarshaled Event:")
	fmt.Printf("Name: %s\n", newEvent.Name)
	fmt.Printf("StartTime: %s\n", newEvent.StartTime)
	fmt.Printf("EndTime: %s\n", newEvent.EndTime)
	fmt.Printf("CreatedAt: %s\n", newEvent.CreatedAt)

	// Test with zero time
	fmt.Println("\n--- Zero Time Test ---")
	zeroEvent := Event{
		Name:      "Zero Time Event",
		StartTime: Time{}, // zero time
		EndTime:   Time{},
		CreatedAt: Time{},
	}
	zeroJSON, _ := json.MarshalIndent(zeroEvent, "", "  ")
	fmt.Println("Zero event JSON:")
	fmt.Println(string(zeroJSON))

	var parsedZero Event
	json.Unmarshal(zeroJSON, &parsedZero)
	fmt.Println("Parsed zero event:")
	fmt.Printf("StartTime: %s\n", parsedZero.StartTime)
	fmt.Printf("EndTime: %s\n", parsedZero.EndTime)
	fmt.Printf("CreatedAt: %s\n", parsedZero.CreatedAt)
}

// Unit tests - run with: go test -v
func TestTimeMarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		timeVal  Time
		expected string
	}{
		{
			name: "non-zero time",
			timeVal: Time{
				Time: time.Date(2026, 8, 7, 14, 30, 0, 0, time.UTC),
			},
			expected: `"2026-08-07 14:30:00"`,
		},
		{
			name:     "zero time",
			timeVal:  Time{},
			expected: "null",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.timeVal.MarshalJSON()
			if err != nil {
				t.Fatalf("MarshalJSON returned error: %v", err)
			}
			if string(data) != tt.expected {
				t.Errorf("MarshalJSON = %s, expected %s", string(data), tt.expected)
			}
		})
	}
}

func TestTimeUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected time.Time
		wantErr  bool
	}{
		{
			name:     "valid time string",
			input:    `"2026-08-07 14:30:00"`,
			expected: time.Date(2026, 8, 7, 14, 30, 0, 0, time.UTC),
			wantErr:  false,
		},
		{
			name:     "null value",
			input:    "null",
			expected: time.Time{},
			wantErr:  false,
		},
		{
			name:     "invalid format",
			input:    `"2026-08-07"`,
			expected: time.Time{},
			wantErr:  true,
		},
		{
			name:     "empty string",
			input:    `""`,
			expected: time.Time{},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tVal Time
			err := tVal.UnmarshalJSON([]byte(tt.input))

			if (err != nil) != tt.wantErr {
				t.Errorf("UnmarshalJSON error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr {
				if !tVal.Time.Equal(tt.expected) {
					t.Errorf("UnmarshalJSON = %v, expected %v", tVal.Time, tt.expected)
				}
			}
		})
	}
}

func TestEventRoundTrip(t *testing.T) {
	original := Event{
		Name: "Test Event",
		StartTime: Time{
			Time: time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC),
		},
		EndTime: Time{
			Time: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC),
		},
		CreatedAt: Time{
			Time: time.Date(2026, 8, 6, 23, 59, 59, 0, time.UTC),
		},
	}

	// Marshal
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Unmarshal
	var result Event
	err = json.Unmarshal(data, &result)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Compare
	if result.Name != original.Name {
		t.Errorf("Name mismatch: got %s, want %s", result.Name, original.Name)
	}
	if !result.StartTime.Equal(original.StartTime.Time) {
		t.Errorf("StartTime mismatch: got %v, want %v", result.StartTime, original.StartTime)
	}
	if !result.EndTime.Equal(original.EndTime.Time) {
		t.Errorf("EndTime mismatch: got %v, want %v", result.EndTime, original.EndTime)
	}
	if !result.CreatedAt.Equal(original.CreatedAt.Time) {
		t.Errorf("CreatedAt mismatch: got %v, want %v", result.CreatedAt, original.CreatedAt)
	}
}
