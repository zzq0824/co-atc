package reference

// AircraftInfo 表示来自 aircraft.csv(wiedehopf/tar1090-db)的飞行器数据
// 格式:Hex;Registration;TypeCode;??;ManufacturerModel;Year;Owner;??
type AircraftInfo struct {
	Hex               string
	Registration      string
	TypeCode          string // ICAO 机型代码(例如 B738)
	ManufacturerModel string // 制造商 + 机型组合(例如 "BOEING 737-800")
	Year              string
	Owner             string
}

// AirlineInfo 表示来自 airlines.dat(OpenFlights)的航司数据
type AirlineInfo struct {
	Name    string
	Country string
}

// AirportInfo 表示来自 airports.csv(OurAirports)的机场数据
type AirportInfo struct {
	ID           int             `json:"id"`
	Ident        string          `json:"ident"`
	Type         string          `json:"type"` // large_airport、medium_airport、small_airport、heliport、closed、seaplane_base
	Name         string          `json:"name"`
	Latitude     float64         `json:"latitude"`
	Longitude    float64         `json:"longitude"`
	ElevationFt  float64         `json:"elevation_ft"`
	Continent    string          `json:"continent"`
	ISOCountry   string          `json:"iso_country"`
	ISORegion    string          `json:"iso_region"`
	Municipality string          `json:"municipality"`
	IATACode     string          `json:"iata_code,omitempty"`
	Frequencies  []FrequencyInfo `json:"frequencies,omitempty"`
}

// FrequencyInfo 表示来自 airport-frequencies.csv 的机场频率
type FrequencyInfo struct {
	ID           int     `json:"id"`
	AirportIdent string  `json:"airport_ident"`
	Type         string  `json:"type"` // TWR、GND、APP、DEP、ATIS、CTAF 等
	Description  string  `json:"description"`
	FrequencyMHz float64 `json:"frequency_mhz"`
}

// RunwayInfo 表示来自 runways.csv(OurAirports)的跑道
type RunwayInfo struct {
	ID            int     `json:"id"`
	AirportIdent  string  `json:"airport_ident"`
	LengthFt      int     `json:"length_ft"`
	WidthFt       int     `json:"width_ft"`
	Surface       string  `json:"surface"`
	Lighted       bool    `json:"lighted"`
	Closed        bool    `json:"closed"`
	LEIdent       string  `json:"le_ident"`
	LELatitude    float64 `json:"le_latitude"`
	LELongitude   float64 `json:"le_longitude"`
	LEElevationFt float64 `json:"le_elevation_ft"`
	LEHeadingDegT float64 `json:"le_heading_deg_t"`
	LEDisplacedFt float64 `json:"le_displaced_ft"`
	HEIdent       string  `json:"he_ident"`
	HELatitude    float64 `json:"he_latitude"`
	HELongitude   float64 `json:"he_longitude"`
	HEElevationFt float64 `json:"he_elevation_ft"`
	HEHeadingDegT float64 `json:"he_heading_deg_t"`
	HEDisplacedFt float64 `json:"he_displaced_ft"`
}

// NavaidInfo 表示来自 navaids.csv(OurAirports)的导航台
type NavaidInfo struct {
	ID                int     `json:"id"`
	Ident             string  `json:"ident"`
	Name              string  `json:"name"`
	Type              string  `json:"type"` // VOR、VOR-DME、VORTAC、NDB、NDB-DME、DME、TACAN
	FrequencyKHz      float64 `json:"frequency_khz"`
	Latitude          float64 `json:"latitude"`
	Longitude         float64 `json:"longitude"`
	ElevationFt       float64 `json:"elevation_ft"`
	ISOCountry        string  `json:"iso_country"`
	MagneticVariation float64 `json:"magnetic_variation"`
	UsageType         string  `json:"usage_type"` // LO、HI、BOTH、TERMINAL
	Power             string  `json:"power"`      // LOW、MEDIUM、HIGH
	AssociatedAirport string  `json:"associated_airport,omitempty"`
}

// RunwayExtensionPoint 表示跑道延长中线上的一个点
type RunwayExtensionPoint struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Distance  float64 `json:"distance"`
}

// ServiceConfig 包含初始化参考服务所需的所有路径与参数
type ServiceConfig struct {
	AircraftCSVPath    string
	AirlinesDATPath    string
	AirportsCSVPath    string
	FrequenciesCSVPath string
	RunwaysCSVPath     string
	NavaidsCSVPath     string
	StationLat         float64
	StationLon         float64
	HomeAirportCode    string
	DisplayRangeNM     float64
	ExtensionLengthNM  float64
}
