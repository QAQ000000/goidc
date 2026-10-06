package legacy

import "strings"

// ServiceNavigationIcon maps recognized legacy classes to fixed web icons.
// Unknown source classes remain archived and use a neutral box icon.
func ServiceNavigationIcon(raw string) (string, bool) {
	aliases := map[string]string{"fa-server": "server", "fa-cloud": "cloud", "fa-database": "database", "fa-globe": "globe", "fa-cube": "box", "fa-cubes": "box", "el-icon-menu": "box", "el-icon-cloudy": "cloud", "el-icon-s-platform": "server", "bx-server": "server", "bxs-server": "server", "bx-cloud": "cloud", "bxs-cloud": "cloud", "bx-data": "database", "bxs-data": "database", "bx-globe": "globe", "bx-box": "box", "bxs-box": "box"}
	if strings.TrimSpace(raw) == "" {
		return "box", true
	}
	for _, class := range strings.Fields(raw) {
		if icon, ok := aliases[class]; ok {
			return icon, true
		}
	}
	return "box", false
}
