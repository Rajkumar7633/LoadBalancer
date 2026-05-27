package advanced_routing

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"loadbalancer/internal/backend"
)

type GeoLocation struct {
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
	Region      string  `json:"region"`
	City        string  `json:"city"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	ISP         string  `json:"isp"`
	ASN         string  `json:"asn"`
}

type GeoRule struct {
	Name        string            `json:"name"`
	Priority    int               `json:"priority"`
	Conditions  GeoConditions     `json:"conditions"`
	Backends    []backend.Backend `json:"backends"`
	Description string            `json:"description"`
}

type GeoConditions struct {
	Countries  []string `json:"countries"`
	Regions    []string `json:"regions"`
	Cities     []string `json:"cities"`
	ISPs       []string `json:"isps"`
	ASNs       []string `json:"asns"`
	Continents []string `json:"continents"`
	Exclude    bool     `json:"exclude"` // If true, exclude matching locations
}

type GeoRouter struct {
	rules       []GeoRule
	backends    []backend.Backend
	geoProvider GeoProvider
	cache       map[string]*GeoLocation
	cacheExpiry time.Duration
	mu          sync.RWMutex
}

type GeoProvider interface {
	GetLocation(ip string) (*GeoLocation, error)
	RefreshDatabase() error
}

type MaxMindGeoProvider struct {
	databasePath string
	mu           sync.RWMutex
}

type GeoConfig struct {
	Enabled         bool          `json:"enabled"`
	DatabasePath    string        `json:"database_path"`
	CacheExpiry     time.Duration `json:"cache_expiry"`
	DefaultRegion   string        `json:"default_region"`
	FallbackBackend string        `json:"fallback_backend"`
}

func NewGeoRouter(config GeoConfig) (*GeoRouter, error) {
	provider, err := NewMaxMindGeoProvider(config.DatabasePath)
	if err != nil {
		return nil, fmt.Errorf("failed to create geo provider: %w", err)
	}

	return &GeoRouter{
		rules:       make([]GeoRule, 0),
		backends:    make([]backend.Backend, 0),
		geoProvider: provider,
		cache:       make(map[string]*GeoLocation),
		cacheExpiry: config.CacheExpiry,
	}, nil
}

func (gr *GeoRouter) SetBackends(backends []backend.Backend) {
	gr.mu.Lock()
	defer gr.mu.Unlock()
	gr.backends = backends
}

func (gr *GeoRouter) AddRule(rule GeoRule) {
	gr.mu.Lock()
	defer gr.mu.Unlock()
	gr.rules = append(gr.rules, rule)
}

func (gr *GeoRouter) Route(ctx context.Context, r *http.Request) backend.Backend {
	clientIP := getClientIP(r)

	// Get geolocation
	geo, err := gr.getGeoLocation(clientIP)
	if err != nil {
		// Fallback to default routing
		return gr.selectDefaultBackend()
	}

	// Sort rules by priority
	sortedRules := gr.getSortedRules()

	// Evaluate rules
	for _, rule := range sortedRules {
		if gr.matchesRule(geo, rule.Conditions) {
			return gr.selectHealthyBackend(rule.Backends)
		}
	}

	// Fallback to default backend
	return gr.selectDefaultBackend()
}

func (gr *GeoRouter) getGeoLocation(ip string) (*GeoLocation, error) {
	gr.mu.RLock()
	if cached, exists := gr.cache[ip]; exists {
		gr.mu.RUnlock()
		return cached, nil
	}
	gr.mu.RUnlock()

	// Fetch from provider
	geo, err := gr.geoProvider.GetLocation(ip)
	if err != nil {
		return nil, err
	}

	// Cache the result
	gr.mu.Lock()
	gr.cache[ip] = geo
	gr.mu.Unlock()

	return geo, nil
}

func (gr *GeoRouter) matchesRule(geo *GeoLocation, conditions GeoConditions) bool {
	matches := false

	// Check countries
	if len(conditions.Countries) > 0 {
		for _, country := range conditions.Countries {
			if strings.EqualFold(geo.Country, country) || strings.EqualFold(geo.CountryCode, country) {
				matches = true
				break
			}
		}
	}

	// Check regions
	if len(conditions.Regions) > 0 {
		for _, region := range conditions.Regions {
			if strings.EqualFold(geo.Region, region) {
				matches = true
				break
			}
		}
	}

	// Check cities
	if len(conditions.Cities) > 0 {
		for _, city := range conditions.Cities {
			if strings.EqualFold(geo.City, city) {
				matches = true
				break
			}
		}
	}

	// Check ISPs
	if len(conditions.ISPs) > 0 {
		for _, isp := range conditions.ISPs {
			if strings.Contains(strings.ToLower(geo.ISP), strings.ToLower(isp)) {
				matches = true
				break
			}
		}
	}

	// Check ASNs
	if len(conditions.ASNs) > 0 {
		for _, asn := range conditions.ASNs {
			if strings.EqualFold(geo.ASN, asn) {
				matches = true
				break
			}
		}
	}

	// Check continents (derived from country codes)
	if len(conditions.Continents) > 0 {
		continent := gr.getContinent(geo.CountryCode)
		for _, cont := range conditions.Continents {
			if strings.EqualFold(continent, cont) {
				matches = true
				break
			}
		}
	}

	// If exclude is true, invert the result
	if conditions.Exclude {
		return !matches
	}

	return matches
}

func (gr *GeoRouter) getContinent(countryCode string) string {
	// Simple continent mapping
	continents := map[string]string{
		"US": "North America", "CA": "North America", "MX": "North America",
		"GB": "Europe", "DE": "Europe", "FR": "Europe", "IT": "Europe", "ES": "Europe",
		"CN": "Asia", "JP": "Asia", "KR": "Asia", "IN": "Asia", "SG": "Asia",
		"BR": "South America", "AR": "South America", "CL": "South America",
		"AU": "Oceania", "NZ": "Oceania",
		"ZA": "Africa", "EG": "Africa", "NG": "Africa",
	}

	if cont, exists := continents[countryCode]; exists {
		return cont
	}
	return "Unknown"
}

func (gr *GeoRouter) getSortedRules() []GeoRule {
	// Sort rules by priority (higher priority first)
	sorted := make([]GeoRule, len(gr.rules))
	copy(sorted, gr.rules)

	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i].Priority < sorted[j].Priority {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	return sorted
}

func (gr *GeoRouter) selectHealthyBackend(backends []backend.Backend) backend.Backend {
	for _, backend := range backends {
		if backend.IsHealthy() && !backend.IsDraining() {
			return backend
		}
	}
	return nil
}

func (gr *GeoRouter) selectDefaultBackend() backend.Backend {
	for _, backend := range gr.backends {
		if backend.IsHealthy() && !backend.IsDraining() {
			return backend
		}
	}
	return nil
}

func (gr *GeoRouter) GetStats() GeoRoutingStats {
	gr.mu.RLock()
	defer gr.mu.RUnlock()

	return GeoRoutingStats{
		CacheSize:     len(gr.cache),
		RulesCount:    len(gr.rules),
		BackendsCount: len(gr.backends),
	}
}

type GeoRoutingStats struct {
	CacheSize     int `json:"cache_size"`
	RulesCount    int `json:"rules_count"`
	BackendsCount int `json:"backends_count"`
}

// MaxMindGeoProvider implementation
func NewMaxMindGeoProvider(databasePath string) (*MaxMindGeoProvider, error) {
	return &MaxMindGeoProvider{
		databasePath: databasePath,
	}, nil
}

func (mp *MaxMindGeoProvider) GetLocation(ip string) (*GeoLocation, error) {
	// In a real implementation, this would use the MaxMind GeoIP2 database
	// For now, return a mock implementation
	return &GeoLocation{
		Country:     "United States",
		CountryCode: "US",
		Region:      "California",
		City:        "San Francisco",
		Latitude:    37.7749,
		Longitude:   -122.4194,
		ISP:         "Example ISP",
		ASN:         "AS12345",
	}, nil
}

func (mp *MaxMindGeoProvider) RefreshDatabase() error {
	// In a real implementation, this would download and update the MaxMind database
	return nil
}

// Validate IP address
func isValidIP(ip string) bool {
	return net.ParseIP(ip) != nil
}
