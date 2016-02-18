// Package fleet implements a fleet client providing basic operations against a
// fleet endpoint through fleet's HTTP API. Higher level scheduling and
// management should be built on top of that.
package fleet

import (
	"fmt"
	"net"
	"net/http"
	"net/url"

	"github.com/coreos/fleet/client"
	"github.com/coreos/fleet/schema"
	"github.com/coreos/fleet/unit"
)

const (
	unitStateInactive = "inactive"
	unitStateLoaded   = "loaded"
	unitStateLaunched = "launched"
)

// Config provides all necessary and injectable configurations for a new
// fleet client.
type Config struct {
	Client   *http.Client
	Endpoint url.URL
}

// DefaultConfig provides a set of configurations with default values by best
// effort.
func DefaultConfig() Config {
	URL, err := url.Parse("file:///var/run/fleet.sock")
	if err != nil {
		panic(err)
	}

	newConfig := Config{
		Client:   http.DefaultClient,
		Endpoint: *URL,
	}

	return newConfig
}

// MachineStatus represents a unit's status scheduled on a certain machine.
type MachineStatus struct {
	// ID represents the machines fleet agent ID where the related unit is
	// running on.
	ID string

	// IP represents the machines IP where the related unit is running on.
	IP net.IP

	// SystemdActive represents the unit's systemd active state.
	SystemdActive string
}

// UnitStatus represents the status of a unit.
type UnitStatus struct {
	// Current represents the current status within the fleet cluster.
	Current string

	// Desired represents the desired status within the fleet cluster.
	Desired string

	// Machine represents the status within a machine. For normal units that are
	// scheduled on only one machine there will be one MachineStatus returned.
	// For global units that are scheduled on multiple machines there will be
	// multiple MachineStatus returned. If a unit is not yet scheduled to any
	// machine, this will be empty.
	Machine []MachineStatus
}

// Fleet defines the interface a fleet client needs to implement to provide
// basic operations against a fleet endpoint.
type Fleet interface {
	// Submit schedules a unit on the configured fleet cluster. This is done by
	// setting the unit's target state to loaded.
	Submit(name, content string) error

	// Start starts a unit on the configured fleet cluster. This is done by
	// setting the unit's target state to launched.
	Start(name string) error

	// Stop stops a unit on the configured fleet cluster. This is done by
	// setting the unit's target state to loaded.
	Stop(name string) error

	// Destroy delets a unit on the configured fleet cluster. This is done by
	// setting the unit's target state to inactive.
	Destroy(name string) error

	// GetStatus fetches the current status of a unit. If the unit cannot be
	// found, an error that you can identify using IsUnitNotFound is returned.
	GetStatus(name string) (UnitStatus, error)
}

// NewFleet creates a new Fleet that is configured with the given settings.
//
//   newConfig := fleet.DefaultConfig()
//   newConfig.Endpoint = myCustomEndpoint
//   newFleet := fleet.NewFleet(newConfig)
//
func NewFleet(config Config) (Fleet, error) {
	var trans http.RoundTripper

	switch config.Endpoint.Scheme {
	case "unix", "file":
		if len(config.Endpoint.Host) > 0 {
			// This commonly happens if the user misses the leading slash after the
			// scheme. For example, "unix://var/run/fleet.sock" would be parsed as
			// host "var".
			return nil, maskAny(fmt.Errorf("unable to connect to host %q with scheme %q", config.Endpoint.Host, config.Endpoint.Scheme))
		}

		// The Path field is only used for dialing and should not be used when
		// building any further HTTP requests.
		sockPath := config.Endpoint.Path
		config.Endpoint.Path = ""

		// http.Client doesn't support the schemes "unix" or "file", but it
		// is safe to use "http" as dialFunc ignores it anyway.
		config.Endpoint.Scheme = "http"

		// The Host field is not used for dialing, but will be exposed in debug logs.
		config.Endpoint.Host = "domain-sock"

		trans = &http.Transport{
			Dial: func(s, t string) (net.Conn, error) {
				// http.Client does not natively support dialing a unix domain socket,
				// so the dial function must be overridden.
				return net.Dial("unix", sockPath)
			},
		}
	case "http", "https":
		trans = http.DefaultTransport
	default:
		return nil, maskAny(fmt.Errorf("invalid scheme in fleet endpoint: %s", config.Endpoint.Scheme))
	}

	config.Client.Transport = trans

	client, err := client.NewHTTPClient(config.Client, config.Endpoint)
	if err != nil {
		return nil, maskAny(err)
	}

	newFleet := fleet{
		Config: config,
		Client: client,
	}

	return newFleet, nil
}

type fleet struct {
	Config Config
	Client client.API
}

func (f fleet) Submit(name, content string) error {
	unitFile, err := unit.NewUnitFile(content)
	if err != nil {
		return maskAny(err)
	}

	unit := &schema.Unit{
		Name:         name,
		Options:      schema.MapUnitFileToSchemaUnitOptions(unitFile),
		DesiredState: "loaded",
	}

	err = f.Client.CreateUnit(unit)
	if err != nil {
		return maskAny(err)
	}

	return nil
}

func (f fleet) Start(name string) error {
	err := f.Client.SetUnitTargetState(name, unitStateLaunched)
	if err != nil {
		return maskAny(err)
	}

	return nil
}

func (f fleet) Stop(name string) error {
	err := f.Client.SetUnitTargetState(name, unitStateLoaded)
	if err != nil {
		return maskAny(err)
	}

	return nil
}

func (f fleet) Destroy(name string) error {
	err := f.Client.DestroyUnit(name)
	if err != nil {
		return maskAny(err)
	}

	return nil
}

func (f fleet) GetStatus(name string) (UnitStatus, error) {
	// Lookup fleet cluster state.
	fleetUnits, err := f.Client.Units()
	if err != nil {
		return UnitStatus{}, maskAny(err)
	}
	var foundFleetUnit *schema.Unit
	for _, fu := range fleetUnits {
		if name == fu.Name {
			foundFleetUnit = fu
			break
		}
	}

	if foundFleetUnit == nil {
		return UnitStatus{}, maskAny(unitNotFoundError)
	}

	// Lookup machine states.
	fleetUnitStates, err := f.Client.UnitStates()
	if err != nil {
		return UnitStatus{}, maskAny(err)
	}
	var foundFleetUnitStates []*schema.UnitState
	for _, fus := range fleetUnitStates {
		if name == fus.Name {
			foundFleetUnitStates = append(foundFleetUnitStates, fus)
		}
	}

	// Aggregate our own unit status.
	ourUnitStatus := UnitStatus{
		Current: foundFleetUnit.CurrentState,
		Desired: foundFleetUnit.DesiredState,
		Machine: []MachineStatus{},
	}
	for _, ffus := range foundFleetUnitStates {
		IP, err := f.ipFromUnitState(ffus)
		if err != nil {
			return UnitStatus{}, maskAny(err)
		}
		ourMachineStatus := MachineStatus{
			ID:            ffus.MachineID,
			IP:            IP,
			SystemdActive: ffus.SystemdActiveState,
		}
		ourUnitStatus.Machine = append(ourUnitStatus.Machine, ourMachineStatus)
	}

	return ourUnitStatus, nil
}

func (f fleet) ipFromUnitState(unitState *schema.UnitState) (net.IP, error) {
	machineStates, err := f.Client.Machines()
	if err != nil {
		return nil, maskAny(err)
	}

	for _, ms := range machineStates {
		if unitState.MachineID == ms.ID {
			return net.ParseIP(ms.PublicIP), nil
		}
	}

	return nil, maskAny(ipNotFoundError)
}