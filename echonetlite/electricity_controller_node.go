package echonetlite

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	gpower = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "home",
			Subsystem: "smartmeter_exporter",
			Name:      "instantpower",
			Help:      "Instantaneous power consumption in watts. Kept for backward compatibility.",
		},
	)
	gInstantPowerW = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "home",
			Subsystem: "smartmeter_exporter",
			Name:      "instant_power_w",
			Help:      "Instantaneous power consumption in watts.",
		},
	)
	gCumulativeEnergyKWh = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "home",
			Subsystem: "smartmeter_exporter",
			Name:      "cumulative_energy_kwh",
			Help:      "Normal-direction cumulative electric energy converted to kWh from E0, D3 and E1.",
		},
	)
	gCumulativeEnergyRaw = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "home",
			Subsystem: "smartmeter_exporter",
			Name:      "cumulative_energy_raw",
			Help:      "Raw normal-direction cumulative electric energy value from E0.",
		},
	)
	gCoefficient = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "home",
			Subsystem: "smartmeter_exporter",
			Name:      "coefficient",
			Help:      "Low-voltage smart meter coefficient from D3. Defaults to 1 when D3 cannot be read.",
		},
	)
	gCumulativeEnergyUnitKWh = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "home",
			Subsystem: "smartmeter_exporter",
			Name:      "cumulative_energy_unit_kwh",
			Help:      "E1 cumulative electric energy unit as a kWh multiplier.",
		},
	)
	gLastSuccessUnixTime = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "home",
			Subsystem: "smartmeter_exporter",
			Name:      "last_success_unixtime",
			Help:      "Unix time when reading smart meter values last succeeded.",
		},
	)
	gLastErrorUnixTime = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "home",
			Subsystem: "smartmeter_exporter",
			Name:      "last_error_unixtime",
			Help:      "Unix time when reading smart meter values last failed.",
		},
	)
	gReadErrorsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Namespace: "home",
			Subsystem: "smartmeter_exporter",
			Name:      "read_errors_total",
			Help:      "Total number of smart meter read errors.",
		},
	)
	gConnected = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: "home",
			Subsystem: "smartmeter_exporter",
			Name:      "connected",
			Help:      "Whether the exporter is connected to the smart meter over B-route. 1 means connected, 0 means disconnected.",
		},
	)
)

func init() {
	prometheus.MustRegister(gpower)
	prometheus.MustRegister(gInstantPowerW)
	prometheus.MustRegister(gCumulativeEnergyKWh)
	prometheus.MustRegister(gCumulativeEnergyRaw)
	prometheus.MustRegister(gCoefficient)
	prometheus.MustRegister(gCumulativeEnergyUnitKWh)
	prometheus.MustRegister(gLastSuccessUnixTime)
	prometheus.MustRegister(gLastErrorUnixTime)
	prometheus.MustRegister(gReadErrorsTotal)
	prometheus.MustRegister(gConnected)
}

// SmartMeterClient is interface for smart-meter cleint
type SmartMeterClient interface {
	Connect(ctx context.Context, bRouteID, bRoutePW string) error
	Close()
	Send(data []byte) ([]byte, error)
}

// ElectricityControllerNode is node for smart-meter
type ElectricityControllerNode struct {
	client          SmartMeterClient
	coefficient     uint32
	coefficientRead bool
	unitKWh         float64
	unitRead        bool
	transID         uint16
}

// NewElectricityControllerNode returns ElectricityControllerNode instance
func NewElectricityControllerNode(c SmartMeterClient) *ElectricityControllerNode {
	return &ElectricityControllerNode{client: c, coefficient: 1}
}

// Close closes client
func (n ElectricityControllerNode) Close() {
	n.client.Close()
}

// Start starts to connect to smart-meter
func (n ElectricityControllerNode) Start(ctx context.Context, bRouteID, bRoutePassword string) error {
	err := n.client.Connect(ctx, bRouteID, bRoutePassword)
	if err != nil {
		gConnected.Set(0)
		return fmt.Errorf("exec Connect failed: %v", err)
	}
	gConnected.Set(1)
	return nil
}

// GetPowerConsumption requests power consumption and receives
func (n *ElectricityControllerNode) GetPowerConsumption() (int, error) {
	power, err := n.readInstantPower()
	if err != nil {
		recordReadError()
		return 0, err
	}
	updateInstantPowerMetrics(power)
	gLastSuccessUnixTime.Set(float64(time.Now().Unix()))
	logger.Printf("Power: %d [W]", power)
	return power, nil
}

// UpdateSmartMeterMetrics updates instant power, cumulative energy and read-state metrics.
func (n *ElectricityControllerNode) UpdateSmartMeterMetrics() error {
	power, err := n.GetPowerConsumption()
	if err != nil {
		return err
	}

	if !n.coefficientRead {
		if err := n.updateCoefficient(); err != nil {
			log.Printf("failed to read smart meter coefficient D3, using 1: %v", err)
			recordReadError()
			n.coefficient = 1
			n.coefficientRead = true
			gCoefficient.Set(1)
		}
	}

	if !n.unitRead {
		if err := n.updateEnergyUnit(); err != nil {
			log.Printf("failed to read smart meter cumulative energy unit E1: %v", err)
			recordReadError()
		}
	}

	if err := n.updateCumulativeEnergy(); err != nil {
		log.Printf("failed to read smart meter cumulative energy E0: %v", err)
		recordReadError()
	}

	logger.Printf("Updated smart meter metrics. instant_power=%d [W]", power)
	return nil
}

func (n *ElectricityControllerNode) readInstantPower() (int, error) {
	p, err := n.readSmartMeterProperty(InstantPower)
	if err != nil {
		return 0, err
	}
	if len(p.Data) != 4 {
		return 0, fmt.Errorf("invalid instant power length: %d", len(p.Data))
	}
	power := binary.BigEndian.Uint32(p.Data)
	return int(power), nil
}

func (n *ElectricityControllerNode) updateCoefficient() error {
	p, err := n.readSmartMeterProperty(LowVoltageSmartMeterCoefficient)
	if err != nil {
		return err
	}
	if len(p.Data) != 4 {
		return fmt.Errorf("invalid coefficient length: %d", len(p.Data))
	}
	n.coefficient = binary.BigEndian.Uint32(p.Data)
	n.coefficientRead = true
	gCoefficient.Set(float64(n.coefficient))
	return nil
}

func (n *ElectricityControllerNode) updateEnergyUnit() error {
	p, err := n.readSmartMeterProperty(IntegralPowerConsumptionUnit)
	if err != nil {
		return err
	}
	if len(p.Data) != 1 {
		return fmt.Errorf("invalid cumulative energy unit length: %d", len(p.Data))
	}
	unit, err := CumulativeEnergyUnitKWh(p.Data[0])
	if err != nil {
		return err
	}
	n.unitKWh = unit
	n.unitRead = true
	gCumulativeEnergyUnitKWh.Set(unit)
	return nil
}

func (n *ElectricityControllerNode) updateCumulativeEnergy() error {
	p, err := n.readSmartMeterProperty(IntegralPowerConsumption)
	if err != nil {
		return err
	}
	if len(p.Data) != 4 {
		return fmt.Errorf("invalid cumulative energy length: %d", len(p.Data))
	}
	raw := binary.BigEndian.Uint32(p.Data)
	gCumulativeEnergyRaw.Set(float64(raw))
	if n.unitRead {
		gCumulativeEnergyKWh.Set(CumulativeEnergyKWh(raw, n.coefficient, n.unitKWh))
	}
	return nil
}

func (n *ElectricityControllerNode) readSmartMeterProperty(code PropertyCode) (Property, error) {
	f := CreateSmartMeterGetFrame(n.nextTransID(), code)

	rdata, err := n.client.Send(f.Serialize())
	if err != nil {
		return Property{}, err
	}
	rf, err := ParseFrame(rdata)
	if err != nil {
		return Property{}, fmt.Errorf("invalid frame: %w", err)
	}
	rf.Print()

	switch rf.ESV {
	// 応答・通知
	case GetRes: // プロパティ値読み出し応答
		o := rf.SrcObj()
		switch o.classGroupCode() {
		case HomeEquipmentGroup:
			switch o.classCode() {
			case LowVoltageSmartMeter:
				for _, p := range rf.Properties {
					switch PropertyCode(p.Code) {
					case code:
						return p, nil
					}
				}
			}
		}
	case GetSNA:
		return Property{}, fmt.Errorf("smart meter returned Get_SNA for EPC 0x%02x", byte(code))
	default:
	}

	return Property{}, fmt.Errorf("smart meter response does not include EPC 0x%02x", byte(code))
}

func (n *ElectricityControllerNode) nextTransID() uint16 {
	n.transID++
	if n.transID == 0 {
		n.transID = 1
	}
	return n.transID
}

func updateInstantPowerMetrics(power int) {
	gpower.Set(float64(power))
	gInstantPowerW.Set(float64(power))
}

func recordReadError() {
	gReadErrorsTotal.Inc()
	gLastErrorUnixTime.Set(float64(time.Now().Unix()))
}

// CumulativeEnergyUnitKWh converts E1 unit code to a kWh multiplier.
func CumulativeEnergyUnitKWh(code byte) (float64, error) {
	switch code {
	case 0x00:
		return 1.0, nil
	case 0x01:
		return 0.1, nil
	case 0x02:
		return 0.01, nil
	case 0x03:
		return 0.001, nil
	case 0x04:
		return 0.0001, nil
	case 0x0A:
		return 10.0, nil
	case 0x0B:
		return 100.0, nil
	case 0x0C:
		return 1000.0, nil
	case 0x0D:
		return 10000.0, nil
	default:
		return 0, fmt.Errorf("unknown cumulative energy unit code: 0x%02x", code)
	}
}

// CumulativeEnergyKWh converts raw E0 cumulative energy to kWh using D3 and E1.
func CumulativeEnergyKWh(raw uint32, coefficient uint32, unitKWh float64) float64 {
	return float64(raw) * float64(coefficient) * unitKWh
}

// CreateCurrentPowerConsumptionFrame creates GET current power consumption frame
func CreateCurrentPowerConsumptionFrame(transID uint16) *Frame {
	return CreateSmartMeterGetFrame(transID, InstantPower)
}

// CreateSmartMeterGetFrame creates a low-voltage smart meter GET frame for one EPC.
func CreateSmartMeterGetFrame(transID uint16, code PropertyCode) *Frame {
	// Get
	src := NewObject(ControllerGroup, Controller, 0x01)
	dest := NewObject(HomeEquipmentGroup, LowVoltageSmartMeter, 0x01)

	props := []Property{}
	props = append(props, Property{Code: byte(code), Len: 0, Data: []byte{}})

	frame := NewFrame(transID, src, dest, Get, props)
	return &frame
}
