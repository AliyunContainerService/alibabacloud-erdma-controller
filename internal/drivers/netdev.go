//go:build linux

package drivers

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/AliyunContainerService/alibabacloud-erdma-controller/internal/types"
	"github.com/samber/lo"
	"github.com/vishvananda/netlink"
)

const (
	ipUrl      = "http://100.100.100.200/latest/meta-data/network/interfaces/macs/%s/primary-ip-address"
	cidrURL    = "http://100.100.100.200/latest/meta-data/network/interfaces/macs/%s/vswitch-cidr-block"
	gatewayURL = "http://100.100.100.200/latest/meta-data/network/interfaces/macs/%s/gateway"

	defaultMetric  = 200
	metricAddition = 1
)

type route struct {
	destination *net.IPNet
	gateway     net.IP
	metric      int
}

type netConf struct {
	ipAddr *net.IPNet
	routes []*route
}

func EnsureNetDevice(link netlink.Link, eri *types.ERI) error {
	if eri.IsPrimaryENI {
		// primary eni not need to config
		return nil
	}
	conf, err := getNetConfFromMetadata(eri.MAC)
	if err != nil {
		return err
	}
	if link.Attrs().OperState != netlink.OperUp {
		driverLog.Info("link down, try to up it", "link", link.Attrs().Name)
		err = ensureUpLink(link)
		if err != nil {
			return err
		}
		defer func() {
			// set link down to make sure reconfig on crashLoop
			if err != nil {
				netlink.LinkSetDown(link) // nolint:errcheck
			}
		}()
		err = ensureAddress(link, conf)
		if err != nil {
			return err
		}
		err = ensureRoutes(link, conf.routes)
		if err != nil {
			return err
		}
		return nil
	} else {
		// if interface is already up, don't config it to avoid traffic disruption, just check it's addr
		addrList, err := netlink.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			return err
		}
		addrConfig := false
		for _, addr := range addrList {
			if addr.IP.String() == conf.ipAddr.IP.String() {
				addrConfig = true
			}
		}
		if !addrConfig {
			driverLog.Error(fmt.Errorf("ip address not found in netlink"), "ip", conf.ipAddr, "link", eri.MAC)
		}
		return nil
	}
}

func ensureUpLink(link netlink.Link) error {
	return netlink.LinkSetUp(link)
}

func ensureAddress(link netlink.Link, conf *netConf) error {
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return err
	}
	for _, addr := range addrs {
		if addr.IP.String() == conf.ipAddr.IP.String() {
			return nil
		}
	}

	err = netlink.AddrAdd(link, &netlink.Addr{IPNet: conf.ipAddr})
	if err != nil {
		return err
	}
	// remove auto create route, fixme when netlink support noprefixroute
	autoCreateRouteCidr := *conf.ipAddr
	autoCreateRouteCidr.IP = autoCreateRouteCidr.IP.Mask(autoCreateRouteCidr.Mask)
	netlink.RouteDel(&netlink.Route{Dst: &autoCreateRouteCidr, LinkIndex: link.Attrs().Index}) // nolint:errcheck
	return nil
}

func ensureRoutes(link netlink.Link, routes []*route) error {
	nlRoutes, err := netlink.RouteList(link, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("list route failed: %v", err)
	}
	directRoute, ok := lo.Find(routes, func(route *route) bool {
		return route.gateway == nil
	})
	if !ok {
		return fmt.Errorf("no direct route found")
	}

	var found bool
	for _, nr := range nlRoutes {
		if nr.Dst != nil && nr.Dst.String() == directRoute.destination.String() {
			found = true
			if directRoute.metric != nr.Priority {
				err = netlink.RouteDel(&nr)
				if err != nil {
					return fmt.Errorf("delete direct route failed: %v", err)
				}
				err = netlink.RouteAdd(&netlink.Route{
					Dst:       directRoute.destination,
					Gw:        directRoute.gateway,
					LinkIndex: link.Attrs().Index,
					Priority:  directRoute.metric,
					Scope:     netlink.SCOPE_LINK,
				})
				if err != nil {
					return fmt.Errorf("update direct route failed: %v", err)
				}
			}
		}
	}
	if !found {
		err = netlink.RouteAdd(&netlink.Route{
			Dst:       directRoute.destination,
			Gw:        directRoute.gateway,
			LinkIndex: link.Attrs().Index,
			Priority:  directRoute.metric,
			Scope:     netlink.SCOPE_LINK,
		})
		if err != nil {
			return fmt.Errorf("create direct route failed: %v", err)
		}
	}

	for _, r := range routes {
		if r.gateway == nil {
			continue
		}
		var found bool
		for _, nr := range nlRoutes {
			if (r.destination == nil && nr.Dst == nil) ||
				(r.destination == nil && nr.Dst.String() == "0.0.0.0/0") ||
				(r.destination != nil && nr.Dst != nil && r.destination.String() == nr.Dst.String()) {
				found = true
				if r.metric != nr.Priority {
					err = netlink.RouteDel(&nr)
					if err != nil {
						return fmt.Errorf("delete route failed: %v", err)
					}
					err = netlink.RouteAdd(&netlink.Route{
						Dst:       r.destination,
						Gw:        r.gateway,
						LinkIndex: link.Attrs().Index,
						Priority:  r.metric,
						Scope:     netlink.SCOPE_LINK,
					})
					if err != nil {
						return fmt.Errorf("update route failed: %v", err)
					}
				}
			}
		}
		if !found {
			err = netlink.RouteAdd(&netlink.Route{
				Dst:       r.destination,
				Gw:        r.gateway,
				LinkIndex: link.Attrs().Index,
				Priority:  r.metric,
			})
			if err != nil {
				return fmt.Errorf("create route failed: %+v, %v, %v", r, r.gateway, err)
			}
		}
	}
	return nil
}

func genRoutesForAddr(gateway net.IP, cidr *net.IPNet) ([]*route, error) {
	existRoutes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return nil, err
	}
	selectMetric := func(r *route) int {
		existMetric := 0
		lo.ForEach(existRoutes, func(route netlink.Route, index int) {
			if (route.Dst == nil && r.destination == nil) ||
				(r.destination == nil && route.Dst.String() == "0.0.0.0/0") ||
				(route.Dst != nil && r.destination != nil && route.Dst.String() == r.destination.String()) {
				if route.Priority > existMetric {
					existMetric = route.Priority
				}
			}
		})
		metric := existMetric + metricAddition
		if metric < defaultMetric {
			metric = defaultMetric
		}
		return metric
	}

	cidrRoute := &route{
		destination: cidr,
		gateway:     nil,
	}
	cidrRoute.metric = selectMetric(cidrRoute)

	defaultRoute := &route{
		destination: nil,
	}
	defaultRoute.metric = selectMetric(defaultRoute)
	defaultRoute.gateway = gateway
	return []*route{cidrRoute, defaultRoute}, nil
}

func getNetConfFromMetadata(mac string) (*netConf, error) {
	addr, err := getStrFromMetadata(fmt.Sprintf(ipUrl, mac))
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return nil, fmt.Errorf("invalid ip address: %s", addr)
	}
	cidr, err := getStrFromMetadata(fmt.Sprintf(cidrURL, mac))
	if err != nil {
		return nil, err
	}
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("invalid cidr: %s", cidr)
	}

	gw, err := getStrFromMetadata(fmt.Sprintf(gatewayURL, mac))
	if err != nil {
		return nil, err
	}
	gateway := net.ParseIP(gw)
	if gateway == nil {
		return nil, fmt.Errorf("invalid gateway address: %s", gw)
	}

	conf := &netConf{
		ipAddr: &net.IPNet{
			IP:   ip,
			Mask: ipNet.Mask,
		},
	}
	routes, err := genRoutesForAddr(gateway, ipNet)
	if err != nil {
		return nil, err
	}
	conf.routes = routes
	return conf, nil
}

func getStrFromMetadata(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("error get url: %s from metaserver. %w", url, err)
	}
	//nolint:errcheck
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("error get url: %s from metaserver, code: %v, %v", url, resp.StatusCode, "Not Found")
	}
	if resp.StatusCode >= http.StatusBadRequest {
		return "", fmt.Errorf("error get url: %s from metaserver, code: %v", url, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	result := strings.Split(string(body), "\n")
	trimResult := strings.Trim(result[0], "/")
	return trimResult, nil
}
