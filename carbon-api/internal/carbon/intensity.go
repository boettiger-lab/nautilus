// Package carbon provides carbon intensity data and emission calculations.
//
// Carbon intensity values (kg CO2/kWh) are sourced from EPA eGRID 2022 subregion
// averages for US grids. The NRP Nautilus cluster spans multiple institutions;
// intensity is looked up by the exported_node hostname suffix when possible,
// falling back to the US average.
//
// Reference: https://www.epa.gov/egrid
package carbon

import "strings"

// intensityByRegion maps hostname keywords → kg CO2 per kWh.
// Keys are matched as case-insensitive substrings of the exported_node label.
// intensityByRegion matches against the DCGM `Hostname` label value,
// e.g. "k8s-a6000-01.csus.edu", "node-1-2.sdsc.edu", "nautilus02.hsrn.nyu.edu".
var intensityByRegion = map[string]float64{
	// California (CAISO / CAMX subregion): SDSC, CSUS, Humboldt, Caltech, UC campuses
	// eGRID CAMX 2022: 0.198 kg CO2/kWh
	".sdsc.":          0.198,
	".csus.":          0.198,
	".humboldt.":      0.198,
	".caltech.":       0.198,
	".ucsd.":          0.198,
	".ucla.":          0.198,
	".ucsb.":          0.198,
	"csumb.":          0.198, // Cal State Monterey Bay (csumb.edu hostname prefix)
	".nrp-nautilus.":  0.198, // NRP cluster is SDSC-hosted; California default
	".calit2.":        0.198, // CalIT2 at UCSD
	"nautilus-":       0.198, // nautilus-* hostnames at SDSC
	"sdsc-":           0.198, // sdsc-* hostnames

	// Nebraska (MROW subregion): UNL (hcc-prp-*, hcc-nrp-*)
	// eGRID MROW 2022: 0.531 kg CO2/kWh
	".unl.": 0.531,

	// New York (NYUP subregion): NYU (hsrn.nyu.edu)
	// eGRID NYUP 2022: 0.174 kg CO2/kWh
	".nyu.": 0.174,

	// Texas (ERCO subregion): UT Austin / TACC
	// eGRID ERCO 2022: 0.393 kg CO2/kWh
	".utexas.": 0.393,
	".tacc.":   0.393,

	// Hawaii (HIOA subregion): UH Manoa
	// eGRID HIOA 2022: 0.702 kg CO2/kWh
	".hawaii.": 0.702,

	// South Carolina (SRSO subregion): Clemson
	// eGRID SRSO 2022: 0.423 kg CO2/kWh
	".clemson.": 0.423,

	// Kansas (SPSO subregion): K-State
	// eGRID SPSO 2022: 0.555 kg CO2/kWh
	".ksu.": 0.555,

	// South Korea: KREONET nodes
	// 2022 grid intensity: 0.459 kg CO2/kWh
	".kreonet.": 0.459,
}

// USAverage is the US national average grid intensity (eGRID 2022).
const USAverage = 0.386 // kg CO2/kWh

// namespaceIntensity maps known namespace prefixes to grid intensity.
// Used as a fallback when the node hostname doesn't match any region.
var namespaceIntensity = map[string]float64{
	"sdsc-": 0.198, // sdsc-llm → SDSC, California (CAMX)
}

// NRPDefault is the fallback intensity for unrecognised NRP nodes.
// The majority of NRP Nautilus nodes are hosted at SDSC (San Diego, CA),
// so California (CAMX, 0.198 kg CO₂/kWh) is a better default than the
// US national average for this cluster.
const NRPDefault = 0.198 // kg CO2/kWh — CAMX (California)

// IntensityForNode returns the grid carbon intensity for a given node hostname.
// namespace is used as a secondary hint when the hostname doesn't match.
// Falls back to NRPDefault (California/CAMX) if both are unknown.
func IntensityForNode(hostname, namespace string) float64 {
	h := strings.ToLower(hostname)
	for keyword, intensity := range intensityByRegion {
		if strings.Contains(h, keyword) {
			return intensity
		}
	}
	ns := strings.ToLower(namespace)
	for prefix, intensity := range namespaceIntensity {
		if strings.HasPrefix(ns, prefix) {
			return intensity
		}
	}
	return NRPDefault
}

// GramsPerHour returns grams of CO2 emitted per hour for a given
// power draw (watts) and grid carbon intensity (kg CO2/kWh).
//
//	g/hr = W / 1000 kW  ×  intensity kg/kWh  ×  1000 g/kg
//	     = W × intensity × 1.0
func GramsPerHour(powerWatts, intensityKgPerKWh float64) float64 {
	return powerWatts * intensityKgPerKWh
}

// MgPerToken returns milligrams of CO2 per output token.
//
//	mg/token = (W / 3.6e6 kWh/s) × intensity kg/kWh × 1e6 mg/kg / (tokens/s)
//	         = W × intensity × (1e6 / 3.6e6) / tokensPerSec
//	         = W × intensity × 0.2778 / tokensPerSec
func MgPerToken(powerWatts, intensityKgPerKWh, tokensPerSec float64) float64 {
	if tokensPerSec <= 0 {
		return 0
	}
	return powerWatts * intensityKgPerKWh * 0.2778 / tokensPerSec
}
