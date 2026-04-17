//go:build windows

package tui

import (
	"os"
	"os/exec"
	"strings"
)

// windowsToIANA maps common Windows timezone names to IANA identifiers.
var windowsToIANA = map[string]string{
	"AUS Central Standard Time":       "Australia/Darwin",
	"AUS Eastern Standard Time":       "Australia/Sydney",
	"Afghanistan Standard Time":       "Asia/Kabul",
	"Alaskan Standard Time":           "America/Anchorage",
	"Arab Standard Time":              "Asia/Riyadh",
	"Arabian Standard Time":           "Asia/Dubai",
	"Arabic Standard Time":            "Asia/Baghdad",
	"Argentina Standard Time":         "America/Argentina/Buenos_Aires",
	"Atlantic Standard Time":          "America/Halifax",
	"Azerbaijan Standard Time":        "Asia/Baku",
	"Azores Standard Time":            "Atlantic/Azores",
	"Canada Central Standard Time":    "America/Regina",
	"Cape Verde Standard Time":        "Atlantic/Cape_Verde",
	"Central America Standard Time":   "America/Guatemala",
	"Central Asia Standard Time":      "Asia/Almaty",
	"Central Brazilian Standard Time": "America/Manaus",
	"Central Europe Standard Time":    "Europe/Budapest",
	"Central European Standard Time":  "Europe/Warsaw",
	"Central Pacific Standard Time":   "Pacific/Noumea",
	"Central Standard Time":           "America/Chicago",
	"Central Standard Time (Mexico)":  "America/Mexico_City",
	"China Standard Time":             "Asia/Shanghai",
	"Cuba Standard Time":              "America/Havana",
	"E. Africa Standard Time":         "Africa/Nairobi",
	"E. Australia Standard Time":      "Australia/Brisbane",
	"E. Europe Standard Time":         "Europe/Bucharest",
	"E. South America Standard Time":  "America/Sao_Paulo",
	"Eastern Standard Time":           "America/New_York",
	"Egypt Standard Time":             "Africa/Cairo",
	"FLE Standard Time":               "Europe/Kyiv",
	"Fiji Standard Time":              "Pacific/Fiji",
	"GMT Standard Time":               "Europe/London",
	"GTB Standard Time":               "Europe/Athens",
	"Georgian Standard Time":          "Asia/Tbilisi",
	"Greenwich Standard Time":         "Africa/Casablanca",
	"Hawaiian Standard Time":          "Pacific/Honolulu",
	"India Standard Time":             "Asia/Kolkata",
	"Iran Standard Time":              "Asia/Tehran",
	"Israel Standard Time":            "Asia/Jerusalem",
	"Jordan Standard Time":            "Asia/Amman",
	"Korea Standard Time":             "Asia/Seoul",
	"Mauritius Standard Time":         "Indian/Mauritius",
	"Middle East Standard Time":       "Asia/Beirut",
	"Montevideo Standard Time":        "America/Montevideo",
	"Morocco Standard Time":           "Africa/Casablanca",
	"Mountain Standard Time":          "America/Denver",
	"Mountain Standard Time (Mexico)": "America/Monterrey",
	"Myanmar Standard Time":           "Asia/Bangkok",
	"N. Central Asia Standard Time":   "Asia/Novosibirsk",
	"Nepal Standard Time":             "Asia/Kathmandu",
	"New Zealand Standard Time":       "Pacific/Auckland",
	"Newfoundland Standard Time":      "America/St_Johns",
	"North Asia East Standard Time":   "Asia/Irkutsk",
	"North Asia Standard Time":        "Asia/Krasnoyarsk",
	"Pacific SA Standard Time":        "America/Santiago",
	"Pacific Standard Time":           "America/Los_Angeles",
	"Pacific Standard Time (Mexico)":  "America/Los_Angeles",
	"Pakistan Standard Time":          "Asia/Karachi",
	"Romance Standard Time":           "Europe/Paris",
	"Russian Standard Time":           "Europe/Moscow",
	"SA Eastern Standard Time":        "America/Sao_Paulo",
	"SA Pacific Standard Time":        "America/Bogota",
	"SA Western Standard Time":        "America/Caracas",
	"SE Asia Standard Time":           "Asia/Bangkok",
	"Samoa Standard Time":             "Pacific/Pago_Pago",
	"Singapore Standard Time":         "Asia/Singapore",
	"South Africa Standard Time":      "Africa/Johannesburg",
	"Sri Lanka Standard Time":         "Asia/Colombo",
	"Taipei Standard Time":            "Asia/Taipei",
	"Tasmania Standard Time":          "Australia/Hobart",
	"Tokyo Standard Time":             "Asia/Tokyo",
	"Tonga Standard Time":             "Pacific/Tongatapu",
	"Turkey Standard Time":            "Asia/Istanbul",
	"US Eastern Standard Time":        "America/New_York",
	"US Mountain Standard Time":       "America/Phoenix",
	"UTC":                             "UTC",
	"Venezuela Standard Time":         "America/Caracas",
	"Vladivostok Standard Time":       "Asia/Vladivostok",
	"W. Australia Standard Time":      "Australia/Perth",
	"W. Central Africa Standard Time": "Africa/Lagos",
	"W. Europe Standard Time":         "Europe/Berlin",
	"West Asia Standard Time":         "Asia/Tashkent",
	"West Pacific Standard Time":      "Pacific/Guam",
	"Yakutsk Standard Time":           "Asia/Yakutsk",
}

// LocalIANATimezone returns the system's IANA timezone name (e.g.
// "America/New_York"). It checks TZ, then queries the Windows registry,
// falling back to "UTC".
func LocalIANATimezone() string {
	if tz := os.Getenv("TZ"); tz != "" && tz != "Local" {
		return tz
	}
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		"(Get-TimeZone).Id").Output()
	if err == nil {
		winTZ := strings.TrimSpace(string(out))
		if iana, ok := windowsToIANA[winTZ]; ok {
			return iana
		}
	}
	return "UTC"
}
