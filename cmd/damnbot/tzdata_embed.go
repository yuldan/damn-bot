//go:build !minimal

package main

import _ "time/tzdata" // PROV-03: embedded IANA in full builds; minimal stays under P13.
