package tor

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/context"

	"github.com/Sirupsen/logrus"
	"github.com/docker/go-plugins-helpers/network"
	"github.com/vishvananda/netlink"
)

// Return the IPv4 address of a network interface
func getIfaceAddr(name string) (*net.IPNet, error) {
	iface, err := netlink.LinkByName(name)
	if err != nil {
		return nil, err
	}
	addrs, err := netlink.AddrList(iface, netlink.FAMILY_V4)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("Interface %s has no IP addresses", name)
	}
	if len(addrs) > 1 {
		logrus.Infof("Interface [ %v ] has more than 1 IPv4 address. Defaulting to using [ %v ]\n", name, addrs[0].IP)
	}
	return addrs[0].IPNet, nil
}

// Set the IP addr of a netlink interface
func setInterfaceIP(name string, rawIP string) error {
	retries := 2
	var iface netlink.Link
	var err error
	for i := 0; i < retries; i++ {
		iface, err = netlink.LinkByName(name)
		if err == nil {
			break
		}
		logrus.Debugf("error retrieving new bridge netlink link [ %s ]... retrying", name)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("Abandoning retrieving the new bridge link from netlink, Run [ ip link ] to troubleshoot the error: %v", err)
	}
	ipNet, err := netlink.ParseIPNet(rawIP)
	if err != nil {
		return err
	}
	addr := &netlink.Addr{IPNet: ipNet, Label: "", Flags: 0, Scope: 0}
	return netlink.AddrAdd(iface, addr)
}

// Create veth pair. Peername is renamed to eth0 in the container
func vethPair(suffix string, bridgeName string) (*netlink.Veth, error) {
	br, err := netlink.LinkByName(bridgeName)
	if err != nil {
		return nil, err
	}

	la := netlink.NewLinkAttrs()
	la.Name = torPortPrefix + suffix
	la.MasterIndex = br.Attrs().Index

	return &netlink.Veth{
		LinkAttrs: la,
		PeerName:  "ethc" + suffix,
	}, nil
}

// Enable a netlink interface
func interfaceUp(name string) error {
	iface, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("Error retrieving a link named [ %s ]: %v", iface.Attrs().Name, err)
	}
	return netlink.LinkSetUp(iface)
}

func truncateID(id string) string {
	return id[:5]
}

func getGenericOptions(r *network.CreateNetworkRequest) map[string]interface{} {
	// Relies on the key always being there. Might need to check the cast was successful?
	// By requiring the network.CreateNetworkRequest we know the the Options should contain the genericOptionsKey
	return r.Options[genericOptionsKey].(map[string]interface{})
}

func getGenericOption(r *network.CreateNetworkRequest, opt string, def string) (string, error) {
	genericOptions := getGenericOptions(r)
	if genericOptions[opt] != nil {
		if value, ok := genericOptions[opt].(string); ok {
			return value, nil
		}
		return "", fmt.Errorf("Casting the value of the %s option to string failed: %v", opt, genericOptions[opt])
	}
	return def, nil
}

func getBridgeMTU(r *network.CreateNetworkRequest) (int, error) {
	mtu, err := getGenericOption(r, mtuOption, "")
	if err != nil {
		return 0, err
	}
	if mtu != "" {
		return strconv.Atoi(mtu)
	}
	return defaultMTU, nil
}

func getBridgeName(r *network.CreateNetworkRequest) (string, error) {
	bridgeName, err := getGenericOption(r, bridgeNameOption, "")
	if err != nil {
		return "", err
	}
	if bridgeName != "" {
		return bridgeName, nil
	}
	return bridgePrefix + truncateID(r.NetworkID), nil
}

func getGatewayIP(r *network.CreateNetworkRequest) (string, string, error) {
	// FIXME: Dear future self, I'm sorry for leaving you with this mess, but I want to get this working ASAP
	// This should be an array
	// We need to handle case where we have
	// a. v6 and v4 - dual stack
	// auxilliary address
	// multiple subnets on one network
	// also in that case, we'll need a function to determine the correct default gateway based on it's IP/Mask
	var gatewayIP string

	if len(r.IPv6Data) > 0 {
		if r.IPv6Data[0] != nil {
			if r.IPv6Data[0].Gateway != "" {
				gatewayIP = r.IPv6Data[0].Gateway
			}
		}
	}
	// Assumption: IPAM will provide either IPv4 OR IPv6 but not both
	// We may want to modify this in future to support dual stack
	if len(r.IPv4Data) > 0 {
		if r.IPv4Data[0] != nil {
			if r.IPv4Data[0].Gateway != "" {
				gatewayIP = r.IPv4Data[0].Gateway
			}
		}
	}

	if gatewayIP == "" {
		return "", "", fmt.Errorf("No gateway IP found")
	}
	parts := strings.Split(gatewayIP, "/")
	if parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("Cannot split gateway IP address")
	}
	return parts[0], parts[1], nil
}

func getRouterName(r *network.CreateNetworkRequest) (string, error) {
	routerName, err := getGenericOption(r, routerNameOption, "")
	if err != nil {
		return "", err
	}
	if routerName != "" {
		return routerName, nil
	}
	// TODO: default to network name + suffix (is the given name available?)
	return "", fmt.Errorf("Routing container not specified: speficy the routing container with the '%s' option", routerNameOption)
}

func (d *Driver) getContainerIP(name string) (string, error) {
	c, err := d.dcli.ContainerInspect(context.Background(), name)
	if err != nil {
		return "", fmt.Errorf("Getting container %s failed: %v", name, err)
	}

	return c.NetworkSettings.IPAddress, nil
}
