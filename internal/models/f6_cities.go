package models

import "strings"

// The Admin Settings city configuration and the Overview constants it scopes.
//
// The city picker is a single-select dropdown of Indonesian cities. The list
// is a closed set held in code rather than a table, for two reasons: it is
// reference data that changes on a human timescale, not operational data, and
// making it a table would invite a second source of truth against the IANA
// zone the coordinated-network report footer already needs. Selecting a city
// therefore sets both `monitored_city` and `city_timezone` in one action.

// SettingMonitoredCity is the cis_settings key holding the single Indonesian
// city this instance monitors.
const SettingMonitoredCity = "monitored_city"

// DefaultMonitoredCity matches DefaultCityTimezone: the Jakarta prototype
// context.
const DefaultMonitoredCity = "Jakarta"

// City is one option of the city dropdown.
type City struct {
	Name string `json:"name"`
	// Province is shown beside the name so "Tangerang" and "Tangerang Selatan"
	// are distinguishable in the dropdown.
	Province string `json:"province"`
	// Timezone is the IANA zone applied to city-local timestamps when this city
	// is selected. Indonesia spans WIB/WITA/WIT.
	Timezone string `json:"timezone"`
}

// IndonesianCities is the closed set the city dropdown selects from.
var IndonesianCities = []City{
	{Name: "Jakarta", Province: "DKI Jakarta", Timezone: "Asia/Jakarta"},
	{Name: "Bandung", Province: "Jawa Barat", Timezone: "Asia/Jakarta"},
	{Name: "Bekasi", Province: "Jawa Barat", Timezone: "Asia/Jakarta"},
	{Name: "Bogor", Province: "Jawa Barat", Timezone: "Asia/Jakarta"},
	{Name: "Depok", Province: "Jawa Barat", Timezone: "Asia/Jakarta"},
	{Name: "Tangerang", Province: "Banten", Timezone: "Asia/Jakarta"},
	{Name: "Semarang", Province: "Jawa Tengah", Timezone: "Asia/Jakarta"},
	{Name: "Yogyakarta", Province: "DI Yogyakarta", Timezone: "Asia/Jakarta"},
	{Name: "Surabaya", Province: "Jawa Timur", Timezone: "Asia/Jakarta"},
	{Name: "Malang", Province: "Jawa Timur", Timezone: "Asia/Jakarta"},
	{Name: "Medan", Province: "Sumatera Utara", Timezone: "Asia/Jakarta"},
	{Name: "Padang", Province: "Sumatera Barat", Timezone: "Asia/Jakarta"},
	{Name: "Pekanbaru", Province: "Riau", Timezone: "Asia/Jakarta"},
	{Name: "Batam", Province: "Kepulauan Riau", Timezone: "Asia/Jakarta"},
	{Name: "Palembang", Province: "Sumatera Selatan", Timezone: "Asia/Jakarta"},
	{Name: "Bandar Lampung", Province: "Lampung", Timezone: "Asia/Jakarta"},
	{Name: "Pontianak", Province: "Kalimantan Barat", Timezone: "Asia/Pontianak"},
	{Name: "Banjarmasin", Province: "Kalimantan Selatan", Timezone: "Asia/Makassar"},
	{Name: "Balikpapan", Province: "Kalimantan Timur", Timezone: "Asia/Makassar"},
	{Name: "Samarinda", Province: "Kalimantan Timur", Timezone: "Asia/Makassar"},
	{Name: "Makassar", Province: "Sulawesi Selatan", Timezone: "Asia/Makassar"},
	{Name: "Manado", Province: "Sulawesi Utara", Timezone: "Asia/Makassar"},
	{Name: "Denpasar", Province: "Bali", Timezone: "Asia/Makassar"},
	{Name: "Mataram", Province: "Nusa Tenggara Barat", Timezone: "Asia/Makassar"},
	{Name: "Kupang", Province: "Nusa Tenggara Timur", Timezone: "Asia/Makassar"},
	{Name: "Ambon", Province: "Maluku", Timezone: "Asia/Jayapura"},
	{Name: "Jayapura", Province: "Papua", Timezone: "Asia/Jayapura"},
}

// FindCity resolves a city by name, case-insensitively.
func FindCity(name string) (City, bool) {
	needle := strings.ToLower(strings.TrimSpace(name))
	for _, c := range IndonesianCities {
		if strings.ToLower(c.Name) == needle {
			return c, true
		}
	}
	return City{}, false
}
