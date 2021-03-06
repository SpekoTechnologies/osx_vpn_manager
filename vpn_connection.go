package main

//todo use keychain to store psks instead of plaintext config file

import (
	"fmt"
	"github.com/gernest/wow"
	"github.com/gernest/wow/spin"
	"github.com/lextoumbourou/goodhosts"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	managedName     = "osx_managed_vpn"
	managedHost     = "managedvpn.local"
	managedPSK      = "osx_managed_psk"
	managedUserName = "osx_managed_un"
	managedPW       = "osx_managed_pw"
	macvpnCMD       = "macosvpn"
	macvpnArgs      = []string{"create",
		"--l2tp",
		managedName,
		"--endpoint",
		managedHost,
		"--username",
		managedUserName,
		"--password",
		managedPW,
		"--shared-secret",
		managedPSK,
		"--split",
		"--force",
	}
	connectionRegex   = regexp.MustCompile(`^Connected`)
	existingHostRegex = regexp.MustCompile(strings.Join([]string{managedHost, "$"}, ""))
	vpcUIDRegex       = regexp.MustCompile(`^vpc-`)
	vpcIndexRegex     = regexp.MustCompile(`\d?`)
	sameConnection    bool
)

func createManagedVPN() {
	cmd := exec.Command(macvpnCMD, macvpnArgs...)
	err := cmd.Run()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Created %s VPN configuration", managedName)
}

func updateManagedVPNHost(vpnHost vpnInstance) {
	hosts, err := goodhosts.NewHosts()
	if err != nil {
		log.Fatal("Could not read hostfile")
	}
	if hosts.Has(vpnHost.PublicIP, managedHost) {
		sameConnection = true
		return
	}
	removeExistingHost()
	addManagedVPNHost(vpnHost)
}

func needsDisconnection() bool {
	var disconnect bool
	if connectionEstablished() {
		if sameConnection {
			disconnect = false
		} else {
			disconnect = true
		}
	}

	return disconnect
}

func disconnectExistingConnection() {
	if needsDisconnection() {
		fmt.Println("Disconnecting existing managed VPN connection")
		disconnectConnection()
	}
}

func disconnectConnection() {
	cmd := exec.Command("scutil",
		"--nc",
		"stop",
		managedName,
	)
	err := cmd.Run()
	if err != nil {
		log.Fatal("Could not stop managed VPN connection")
	}
}

func addManagedVPNHost(vpnHost vpnInstance) {
	hosts, err := goodhosts.NewHosts()
	if err != nil {
		log.Fatal("Could not read hostfile")
	}
	err = hosts.Add(vpnHost.PublicIP, managedHost)
	if err != nil {
		log.Fatal("Could not add entry to host file")
	}
	if err := hosts.Flush(); err != nil {
		log.Fatalf("Error writing host entry %s", err)
	}
}

func removeExistingHost() {
	hosts, err := goodhosts.NewHosts()
	if err != nil {
		log.Fatal("Could not read hostfile")
	}
	for _, hostLine := range hosts.Lines {
		if existingHostRegex.MatchString(hostLine.Raw) {
			fmt.Printf("Removing `%s` from hostfile\n", hostLine.Raw)
			err = hosts.Remove(hostLine.IP, hostLine.Hosts[0])
			if err != nil {
				log.Fatal("Could not remove old host entry")
			}
		}
	}
	if err := hosts.Flush(); err != nil {
		log.Fatalf("Error writing host entry %s", err)
	}
}

func establishManagedVPNConnection(vpnDetails vpnProfile, vpnHost *vpnInstance) {
	cmd := exec.Command("scutil",
		"--nc",
		"start",
		managedName,
		"--user",
		vpnDetails.UserName,
		"--password",
		vpnDetails.PassWord,
		"--secret",
		vpnDetails.Psk)
	err := cmd.Run()
	if err != nil {
		log.Fatalf("Could not connect to vpn via scutil: %s", err)
	}
	i := 0
	print("connecting...")
	w := wow.New(os.Stdout, spin.Get(spin.BouncingBall), " Connecting")
	w.Start()
	for {
		if connectionEstablished() {
			w.Text(" Updating route table").Spinner(spin.Get(spin.Clock))
			updateRouting(*vpnHost)
			w.Stop()
			w.PersistWith(spin.Spinner{Frames: []string{"✅"}}, " Updating route table")
			w.PersistWith(spin.Spinner{Frames: []string{"✅"}}, fmt.Sprintf(" VPN connection to %s established!!", vpnHost.Name))
			break
		} else if i < 20 {
			i++
			time.Sleep(500 * time.Millisecond)
		} else {
			w.Stop()
			w.PersistWith(spin.Spinner{Frames: []string{"‼️"}}, fmt.Sprintf(" Could not establish connection to VPN Host: %s", vpnHost.Name))
			break
		}
	}
}

func verifyManagedVPNConnection() bool {
	cmd := exec.Command("scutil",
		"--nc",
		"show",
		managedName,
	)
	err := cmd.Run()
	if err != nil {
		return false
	}
	return true
}

func setupManagedVPNConnection() {
	if verifyManagedVPNConnection() {
		return
	}
	log.Printf("Managed VPN `%s` not found, creating...\n", managedName)
	createManagedVPN()
	if verifyManagedVPNConnection() {
		fmt.Println("Managed VPN settings applied, please rerun last command\n")
		os.Exit(0)
	}
	log.Fatal("Could not setup managed VPN connection\n")
}

func connectionEstablished() bool {
	output, err := exec.Command("scutil", "--nc", "status", managedName).Output()
	if err != nil {
		log.Fatal(err)
	}
	if connectionRegex.MatchString(string(output)) {
		return true
	}
	return false
}

func updateRouting(vpnHost vpnInstance) {
	cmd := exec.Command("route", "-v", "add", "-net", vpnHost.VpcCidr, "-interface", "ppp0")
	err := cmd.Run()
	if err != nil {
		log.Fatalf("Could not update route table after VPN connection: %s\n", err.Error())
	}
}

func selectVPNHost(identifier string) vpnInstance {
	vpnHostsList := readHostsJSONFile()
	if vpcUIDRegex.MatchString(identifier) {
		fmt.Println("Connecting to VPN by UID")
		for _, host := range vpnHostsList {
			if host.VpcID == identifier {
				return host
			}
		}
	}
	if vpcIndexRegex.MatchString(identifier) {
		fmt.Println("Connecting to VPN by ID #")
		for index, host := range vpnHostsList {
			if strconv.Itoa(index) == identifier {
				return host
			}
		}
	}
	fmt.Println("Connecting to VPN by instance Name")
	for _, host := range vpnHostsList {
		if host.Name == identifier {
			return host
		}
	}
	log.Fatal("Could not find VPN with provided identifier")
	return vpnInstance{}
}

func startConnection(vpnIdentifier string, profileName string) {
	setupManagedVPNConnection()
	vpnHost := selectVPNHost(vpnIdentifier)
	updateManagedVPNHost(vpnHost)
	disconnectExistingConnection()
	profile := selectVPNProfileDetails(profileName)
	establishManagedVPNConnection(profile, &vpnHost)
}
