// Copyright 2018 CNI authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/containernetworking/cni/pkg/skel"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/cni/pkg/version"
	bv "github.com/containernetworking/plugins/pkg/utils/buildversion"
)

// The top-level network config - IPAM plugins are passed the full configuration
// of the calling plugin, not just the IPAM section.
type Net struct {
	Name       string      `json:"name"`
	CNIVersion string      `json:"cniVersion"`
	IPAM       *IPAMConfig `json:"ipam"`

	RuntimeConfig struct {
		IPs []string `json:"ips,omitempty"`
	} `json:"runtimeConfig,omitempty"`
	Args *struct {
		A *IPAMArgs `json:"cni"`
	} `json:"args"`
}

type IPAMConfig struct {
	Name      string
	Type      string         `json:"type"`
	Routes    []*types.Route `json:"routes"`
	Addresses []Address      `json:"addresses,omitempty"`
	DNS       types.DNS      `json:"dns"`
}

type IPAMEnvArgs struct {
	types.CommonArgs
	IP      types.UnmarshallableString `json:"ip,omitempty"`
	GATEWAY types.UnmarshallableString `json:"gateway,omitempty"`
}

type IPAMArgs struct {
	IPs []string `json:"ips"`
}

type Address struct {
	AddressStr string `json:"address"`
	Gateway    net.IP `json:"gateway,omitempty"`
	Address    net.IPNet
	Version    string
}

func main() {
	skel.PluginMain(cmdAdd, cmdCheck, cmdDel, version.All, bv.BuildString("static"))
}

func loadNetConf(bytes []byte) (*types.NetConf, string, error) {
	n := &types.NetConf{}
	if err := json.Unmarshal(bytes, n); err != nil {
		return nil, "", fmt.Errorf("failed to load netconf: %v", err)
	}
	return n, n.CNIVersion, nil
}

func cmdCheck(args *skel.CmdArgs) error {
	ipamConf, _, err := LoadIPAMConfig(args.StdinData, args.Args)
	if err != nil {
		return err
	}

	// Get PrevResult from stdin... store in RawPrevResult
	n, _, err := loadNetConf(args.StdinData)
	if err != nil {
		return err
	}

	// Parse previous result.
	if n.RawPrevResult == nil {
		return fmt.Errorf("Required prevResult missing")
	}

	if err := version.ParsePrevResult(n); err != nil {
		return err
	}

	result, err := current.NewResultFromResult(n.PrevResult)
	if err != nil {
		return err
	}

	// Each configured IP should be found in result.IPs
	for _, rangeset := range ipamConf.Addresses {
		for _, ips := range result.IPs {
			// Ensure values are what we expect
			if rangeset.Address.IP.Equal(ips.Address.IP) {
				if rangeset.Gateway == nil {
					break
				} else if rangeset.Gateway.Equal(ips.Gateway) {
					break
				}
				return fmt.Errorf("static: Failed to match addr %v on interface %v", ips.Address.IP, args.IfName)
			}
		}
	}

	return nil
}

// canonicalizeIP makes sure a provided ip is in standard form
func canonicalizeIP(ip *net.IP) error {
	if ip.To4() != nil {
		*ip = ip.To4()
		return nil
	} else if ip.To16() != nil {
		*ip = ip.To16()
		return nil
	}
	return fmt.Errorf("IP %s not v4 nor v6", *ip)
}

// LoadIPAMConfig creates IPAMConfig using json encoded configuration provided
// as `bytes`. At the moment values provided in envArgs are ignored so there
// is no possibility to overload the json configuration using envArgs
func LoadIPAMConfig(bytes []byte, envArgs string) (*IPAMConfig, string, error) {
	n := Net{}
	if err := json.Unmarshal(bytes, &n); err != nil {
		return nil, "", err
	}
	if n.IPAM == nil {
		return nil, "", fmt.Errorf("IPAM config missing 'ipam' key")
	}

	// load IP from CNI_ARGS
	if envArgs != "" {
		e := IPAMEnvArgs{}
		err := types.LoadArgs(envArgs, &e)
		if err != nil {
			return nil, "", err
		}

		if e.IP != "" {
			for _, item := range strings.Split(string(e.IP), ",") {
				addrStr := strings.TrimSpace(item)

				_, addr, err := net.ParseCIDR(addrStr)
				if err != nil {
					return nil, "", fmt.Errorf("the 'ip' field is expected to be in CIDR notation, got: '%s'", addrStr)
				}

				n.IPAM.Addresses = append(n.IPAM.Addresses, Address{AddressStr: addrStr, Address: *addr})
			}
		}

		if e.GATEWAY != "" {
			for _, item := range strings.Split(string(e.GATEWAY), ",") {
				gwip := net.ParseIP(strings.TrimSpace(item))
				if gwip == nil {
					return nil, "", fmt.Errorf("invalid gateway address: %s", item)
				}

				for i := range n.IPAM.Addresses {
					if n.IPAM.Addresses[i].Address.Contains(gwip) {
						n.IPAM.Addresses[i].Gateway = gwip
					}
				}
			}
		}
	}

	// import address from args
	if n.Args != nil && n.Args.A != nil && len(n.Args.A.IPs) != 0 {
		// args IP overwrites IP, so clear IPAM Config
		n.IPAM.Addresses = make([]Address, 0, len(n.Args.A.IPs))
		for _, addrStr := range n.Args.A.IPs {
			ip, addr, err := net.ParseCIDR(addrStr)
			if err != nil {
				return nil, "", fmt.Errorf("an entry in the 'ips' field is NOT in CIDR notation, got: '%s'", addrStr)
			}
			addr.IP = ip
			n.IPAM.Addresses = append(n.IPAM.Addresses, Address{AddressStr: addrStr, Address: *addr})
		}
	}

	// import address from runtimeConfig
	if len(n.RuntimeConfig.IPs) != 0 {
		// runtimeConfig IP overwrites IP, so clear IPAM Config
		n.IPAM.Addresses = make([]Address, 0, len(n.RuntimeConfig.IPs))
		for _, addrStr := range n.RuntimeConfig.IPs {
			ip, addr, err := net.ParseCIDR(addrStr)
			if err != nil {
				return nil, "", fmt.Errorf("an entry in the 'ips' field is NOT in CIDR notation, got: '%s'", addrStr)
			}
			addr.IP = ip
			n.IPAM.Addresses = append(n.IPAM.Addresses, Address{AddressStr: addrStr, Address: *addr})
		}
	}

	// Validate all ranges
	numV4 := 0
	numV6 := 0

	for i := range n.IPAM.Addresses {
		if n.IPAM.Addresses[i].Address.IP == nil {
			ip, addr, err := net.ParseCIDR(n.IPAM.Addresses[i].AddressStr)
			if err != nil {
				return nil, "", fmt.Errorf(
					"the 'address' field is expected to be in CIDR notation, got: '%s'", n.IPAM.Addresses[i].AddressStr)
			}
			n.IPAM.Addresses[i].Address = *addr
			n.IPAM.Addresses[i].Address.IP = ip
		}

		if err := canonicalizeIP(&n.IPAM.Addresses[i].Address.IP); err != nil {
			return nil, "", fmt.Errorf("invalid address %d: %s", i, err)
		}

		if n.IPAM.Addresses[i].Address.IP.To4() != nil {
			numV4++
		} else {
			numV6++
		}
	}

	// CNI spec 0.2.0 and below supported only one v4 and v6 address
	if numV4 > 1 || numV6 > 1 {
		if ok, _ := version.GreaterThanOrEqualTo(n.CNIVersion, "0.3.0"); !ok {
			return nil, "", fmt.Errorf("CNI version %v does not support more than 1 address per family", n.CNIVersion)
		}
	}

	// Copy net name into IPAM so not to drag Net struct around
	n.IPAM.Name = n.Name

	return n.IPAM, n.CNIVersion, nil
}

func cmdAdd(args *skel.CmdArgs) error {
	ipamConf, confVersion, err := LoadIPAMConfig(args.StdinData, args.Args)
	if err != nil {
		return err
	}

	result := &current.Result{
		CNIVersion: current.ImplementedSpecVersion,
		DNS:        ipamConf.DNS,
		Routes:     ipamConf.Routes,
	}
	for _, v := range ipamConf.Addresses {
		result.IPs = append(result.IPs, &current.IPConfig{
			Address: v.Address,
			Gateway: v.Gateway,
		})
	}

	return types.PrintResult(result, confVersion)
}

func cmdDel(args *skel.CmdArgs) error {
	// Nothing required because of no resource allocation in static plugin.
	return nil
}
