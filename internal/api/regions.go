package api

// regionGeo maps a "provider:region" key to the approximate geographic
// location of that cloud region's datacenter, plus a human place label. Used
// by handleTopology to plot region pins on the world map. Coordinates are
// deterministic (no runtime geolocation call), consistent with the platform's
// generation-based, auditable design. Unlisted regions still appear in the
// Grid view; they just render without a map pin (Located=false).
var regionGeo = map[string]geoPoint{
	// ---- AWS ----
	"aws:us-east-1":      {38.9, -77.4, "N. Virginia"},
	"aws:us-east-2":      {40.0, -83.0, "Ohio"},
	"aws:us-west-1":      {37.4, -121.9, "N. California"},
	"aws:us-west-2":      {45.8, -119.7, "Oregon"},
	"aws:ca-central-1":   {45.5, -73.6, "Montreal"},
	"aws:eu-west-1":      {53.3, -6.3, "Ireland"},
	"aws:eu-west-2":      {51.5, -0.1, "London"},
	"aws:eu-west-3":      {48.9, 2.35, "Paris"},
	"aws:eu-central-1":   {50.1, 8.7, "Frankfurt"},
	"aws:eu-north-1":     {59.3, 18.1, "Stockholm"},
	"aws:eu-south-1":     {45.5, 9.2, "Milan"},
	"aws:ap-south-1":     {19.1, 72.9, "Mumbai"},
	"aws:ap-southeast-1": {1.35, 103.8, "Singapore"},
	"aws:ap-southeast-2": {-33.9, 151.2, "Sydney"},
	"aws:ap-northeast-1": {35.7, 139.7, "Tokyo"},
	"aws:ap-northeast-2": {37.6, 127.0, "Seoul"},
	"aws:ap-east-1":      {22.3, 114.2, "Hong Kong"},
	"aws:sa-east-1":      {-23.5, -46.6, "São Paulo"},
	"aws:af-south-1":     {-33.9, 18.4, "Cape Town"},
	"aws:me-south-1":     {26.1, 50.5, "Bahrain"},

	// ---- Azure ----
	"azure:eastus":             {37.4, -79.8, "East US (Virginia)"},
	"azure:eastus2":            {36.7, -78.4, "East US 2 (Virginia)"},
	"azure:westus":             {37.8, -122.4, "West US (California)"},
	"azure:westus2":            {47.2, -119.9, "West US 2 (Washington)"},
	"azure:westus3":            {33.4, -112.1, "West US 3 (Arizona)"},
	"azure:centralus":          {41.6, -93.6, "Central US (Iowa)"},
	"azure:northeurope":        {53.3, -6.3, "North Europe (Ireland)"},
	"azure:westeurope":         {52.4, 4.9, "West Europe (Netherlands)"},
	"azure:uksouth":            {51.5, -0.1, "UK South (London)"},
	"azure:ukwest":             {51.5, -3.2, "UK West (Cardiff)"},
	"azure:norwayeast":         {59.9, 10.7, "Norway East (Oslo)"},
	"azure:swedencentral":      {60.7, 17.1, "Sweden Central"},
	"azure:francecentral":      {48.9, 2.35, "France Central (Paris)"},
	"azure:germanywestcentral": {50.1, 8.7, "Germany West Central"},
	"azure:switzerlandnorth":   {47.4, 8.5, "Switzerland North (Zurich)"},
	"azure:southeastasia":      {1.35, 103.8, "Southeast Asia (Singapore)"},
	"azure:eastasia":           {22.3, 114.2, "East Asia (Hong Kong)"},
	"azure:australiaeast":      {-33.9, 151.2, "Australia East (Sydney)"},
	"azure:japaneast":          {35.7, 139.7, "Japan East (Tokyo)"},
	"azure:centralindia":       {18.5, 73.9, "Central India (Pune)"},
	"azure:brazilsouth":        {-23.5, -46.6, "Brazil South (São Paulo)"},
	"azure:uaenorth":           {25.3, 55.4, "UAE North (Dubai)"},
	"azure:southafricanorth":   {-26.2, 28.0, "South Africa North"},
	"azure:canadacentral":      {43.7, -79.4, "Canada Central (Toronto)"},
}
