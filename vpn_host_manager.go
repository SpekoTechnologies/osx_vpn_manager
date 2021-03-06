package main

import (
	"encoding/json"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/olekukonko/tablewriter"
	"io/ioutil"
	"log"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go/aws/credentials"
	"os"
	"sort"
	"strconv"
	"sync"
)

var awsRegions = []string{"us-east-1", "us-west-1", "us-west-2", "eu-west-1", "eu-central-1", "sa-east-1"}
var hostFilePath = path.Join(resourcePath, "vpn_hosts.json")
var vpnInstanceFieldNames = []string{"ID #", "VPC ID", "VPN Name", "Environment", "Public IP", "VPC CIDR"}

type vpnInstance struct {
	VpcID       string `json:"vpc_id"`
	Name        string `json:"name"`
	Environment string `json:"environment"`
	PublicIP    string `json:"public_ip"`
	VpcCidr     string `json:"vpc_cidr"`
}
type vpnInstanceGrp []vpnInstance

func listVPCs(profile string) map[string]string {
	type o struct {
		vpcid, vpcidr string
	}
	vpcList := make(map[string]string)
	var wg sync.WaitGroup
	resChan := make(chan o)
	go func(res chan o) {
		for a := range res {
			vpcList[a.vpcid] = a.vpcidr
		}
	}(resChan)
	for _, region := range awsRegions {
		wg.Add(1)
		go func(profile string, reg string, x *sync.WaitGroup, c chan o) {
			fmt.Printf("fetching vpc details for region: %v\n", reg)
			session, err := session.NewSession(&aws.Config{Region: aws.String(reg),
				Credentials: credentials.NewCredentials(&credentials.SharedCredentialsProvider{
					Profile: profile,
				}),
			})
			if err != nil {
				log.Fatalln("Could not establish new AWS session", err)
			}
			svc := ec2.New(session)
			params := &ec2.DescribeVpcsInput{}
			resp, err := svc.DescribeVpcs(params)
			if err != nil {
				fmt.Println("there was an error listing vpcs in", reg, err.Error())
				log.Fatal(err.Error())
			}
			for _, vpc := range resp.Vpcs {
				vpcID := *vpc.VpcId
				vpcCIDR := *vpc.CidrBlock
				c <- o{vpcID, vpcCIDR}
			}
			x.Done()
		}(profile, region, &wg, resChan)
	}
	wg.Wait()
	close(resChan)
	return vpcList
}

func listFilteredInstances(nameFilter string, profile string) []*ec2.Instance {
	var filteredInstances []*ec2.Instance
	var instanceWG sync.WaitGroup
	instanceResChan := make(chan *ec2.Instance)
	go func(res chan *ec2.Instance) {
		for a := range res {
			filteredInstances = append(filteredInstances, a)
		}
	}(instanceResChan)
	for _, region := range awsRegions {
		instanceWG.Add(1)
		go func(profile string, reg string, x *sync.WaitGroup, ic chan *ec2.Instance) {
			session, err := session.NewSession(&aws.Config{Region: aws.String(reg),
				Credentials: credentials.NewCredentials(&credentials.SharedCredentialsProvider{
					Profile: profile,
				}),
			})
			if err != nil {
				log.Fatalln("Could not establish new AWS session", err)
			}
			svc := ec2.New(session)
			fmt.Printf("fetching instances with tag %v in: %v\n", nameFilter, reg)
			params := &ec2.DescribeInstancesInput{
				Filters: []*ec2.Filter{
					{
						Name: aws.String("tag:Name"),
						Values: []*string{
							aws.String(strings.Join([]string{"*", nameFilter, "*"}, "")),
						},
					},
					{
						Name: aws.String("instance-state-name"),
						Values: []*string{
							aws.String("running"),
						},
					},
				},
			}
			resp, err := svc.DescribeInstances(params)
			if err != nil {
				fmt.Println("there was an error listing instnaces in", reg, err.Error())
				log.Fatal(err.Error())
			}
			for _, reservation := range resp.Reservations {
				for _, instance := range reservation.Instances {
					ic <- instance
				}
			}
			x.Done()
		}(profile, region, &instanceWG, instanceResChan)
	}
	instanceWG.Wait()
	close(instanceResChan)
	return filteredInstances
}

func extractTagValue(tagList []*ec2.Tag, lookup string) string {
	tagVale := ""
	for _, tag := range tagList {
		if *tag.Key == lookup {
			tagVale = *tag.Value
			break
		}
	}
	return tagVale
}

func listVpnInstnaces(vpcCidrs map[string]string, profile string) vpnInstanceGrp {
	var vpnInstances vpnInstanceGrp
	vpnInstanceList := listFilteredInstances("vpn", profile)
	for _, instance := range vpnInstanceList {
		if DEBUG {
			fmt.Printf("%+v\n\n", instance)
		}
		vpn := vpnInstance{
			VpcID:       *instance.VpcId,
			VpcCidr:     vpcCidrs[*instance.VpcId],
			Name:        extractTagValue(instance.Tags, "Name"),
			Environment: extractTagValue(instance.Tags, "environment"),
			PublicIP:    *instance.PublicIpAddress,
		}
		vpnInstances = append(vpnInstances, vpn)
	}
	return vpnInstances
}

func writevpnDetailFile(vpnList vpnInstanceGrp) {
	vpnJSON, err := json.Marshal(vpnList)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("Writing host file to %s\n", hostFilePath)
	werror := ioutil.WriteFile(hostFilePath, vpnJSON, 0755)
	if werror != nil {
		fmt.Printf("Could not write host file to path %s\n", hostFilePath)
		log.Fatal(werror)
	}
}

func refreshHosts() {
	awsProfiles := awsProfiles()
	var vpnHostList vpnInstanceGrp
	for _, awsProfile := range awsProfiles {
		fmt.Printf("Refreshing hosts list for profile: %s\n", awsProfile)
		vpcList := listVPCs(awsProfile)
		vpn := listVpnInstnaces(vpcList, awsProfile)
		//todo, add profile to instances. create function to do so
		vpnHostList = append(vpnHostList, vpn...)
		fmt.Println("======")
	}
	writevpnDetailFile(vpnHostList)
	fmt.Println("complete\n")
}

func readHostsJSONFile() vpnInstanceGrp {
	file, e := ioutil.ReadFile(hostFilePath)
	if e != nil {
		fmt.Printf("File error: %v\n", e)
		os.Exit(1)
	}
	var vpnHosts vpnInstanceGrp
	err := json.Unmarshal(file, &vpnHosts)
	if err != nil {
		log.Fatal("Could not read VPN host list")
	}
	sort.Sort(vpnHosts)
	return vpnHosts
}

func printVPNHostList() {
	vpnHostsList := readHostsJSONFile()
	consoleTable := tablewriter.NewWriter(os.Stdout)
	consoleTable.SetHeader(vpnInstanceFieldNames)
	for index, vpnHost := range vpnHostsList {
		row := []string{
			strconv.Itoa(index),
			vpnHost.VpcID,
			vpnHost.Name,
			vpnHost.Environment,
			vpnHost.PublicIP,
			vpnHost.VpcCidr,
		}
		consoleTable.Append(row)
	}
	consoleTable.Render()
}

func (slice vpnInstanceGrp) Len() int {
	return len(slice)
}

func (slice vpnInstanceGrp) Less(i, j int) bool {
	return slice[i].Name < slice[j].Name
}

func (slice vpnInstanceGrp) Swap(i, j int) {
	slice[i], slice[j] = slice[j], slice[i]
}
