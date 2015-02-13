/***
Copyright 2014 Cisco Systems Inc. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"strings"
	"time"

	"github.com/contiv/netplugin/core"
	"github.com/contiv/netplugin/drivers"
	"github.com/contiv/netplugin/gstate"
)

type ConfigEpJson struct {
	Container string
	Host      string
	// XXX: need to think more, if interface name really belongs to logical
	// config. One usecase for having interface name in logical config might be
	// the SRIOV case, where the virtual interfaces could be pre-exist.
	Intf string
}

type ConfigNetworkJson struct {
	Name string
	// XXX: need to think more, if the pkt-tag really belongs to logical
	// config. One usecase for having tag in logical config might be
	// the case of environment where tags are managed outside the realm of
	// allocator. Eg. ACI kind of deployments wjere APIC allocates the tags.
	PktTag    string
	Endpoints []ConfigEpJson
}

type ConfigTenantJson struct {
	Name           string
	DefaultNetType string
	SubnetPool     string
	AllocSubnetLen uint
	Vlans          string
	Vxlans         string

	Networks []ConfigNetworkJson
}

type ConfigJson struct {
	Tenants []ConfigTenantJson
}

func getEpName(net *ConfigNetworkJson, ep *ConfigEpJson) string {
	if ep.Container != "" {
		return net.Name + "-" + ep.Container
	} else {
		return ep.Host + "-native-intf"
	}
}

func postProcessing() {
	time.Sleep(1 * time.Second)
}

func tenantPresent(allCfg *ConfigJson, tenantId string) bool {
	for _, tenant := range allCfg.Tenants {
		if tenantId == tenant.Name {
			return true
		}
	}

	return false
}

func netPresent(allCfg *ConfigJson, netId string) bool {
	for _, tenant := range allCfg.Tenants {
		for _, net := range tenant.Networks {
			if netId == net.Name {
				return true
			}
		}
	}

	return false
}

func epPresent(allCfg *ConfigJson, epId string) bool {
	for _, tenant := range allCfg.Tenants {
		for _, net := range tenant.Networks {
			for _, ep := range net.Endpoints {
				if epId == getEpName(&net, &ep) {
					return true
				}
			}
		}
	}

	return false
}

func initEtcd(defOpts *cliOpts) (*drivers.EtcdStateDriver, error) {
	etcdDriver := &drivers.EtcdStateDriver{}
	driverConfig := &drivers.EtcdStateDriverConfig{}
	driverConfig.Etcd.Machines = []string{defOpts.etcdUrl}
	config := &core.Config{V: driverConfig}
	err := etcdDriver.Init(config)
	return etcdDriver, err
}

func deleteDelta(allCfg *ConfigJson, defOpts *cliOpts) error {

	etcdDriver, err := initEtcd(defOpts)
	if err != nil {
		log.Fatalf("Failed to init etcd driver. Error: %s", err)
	}

	keys, err := etcdDriver.ReadRecursive(drivers.EP_CFG_PATH_PREFIX)
	if err != nil {
		return core.ErrIfKeyExists(err)
	}
	for _, key := range keys {
		epId := strings.TrimPrefix(key, drivers.EP_CFG_PATH_PREFIX)
		if !epPresent(allCfg, epId) {
			opts := *defOpts
			opts.construct.Set(CLI_CONSTRUCT_EP)
			opts.oper.Set(CLI_OPER_DELETE)
			opts.idStr = epId
			log.Printf("deleting ep %s \n", epId)

			err = executeOpts(&opts)
			if err != nil {
				log.Printf("error '%s' deleting ep %s \n", err, epId)
			}
			postProcessing()
		}
	}

	keys, err = etcdDriver.ReadRecursive(drivers.NW_CFG_PATH_PREFIX)
	if err != nil {
		return err
	}
	for _, key := range keys {
		netId := strings.TrimPrefix(key, drivers.NW_CFG_PATH_PREFIX)
		if !netPresent(allCfg, netId) {
			opts := *defOpts
			opts.construct.Set(CLI_CONSTRUCT_NW)
			opts.oper.Set(CLI_OPER_DELETE)
			opts.idStr = netId
			log.Printf("deleting net %s\n", netId)

			err = executeOpts(&opts)
			if err != nil {
				log.Printf("error '%s' deleting net %s \n", err, netId)
			}
			postProcessing()
		}
	}

	keys, err = etcdDriver.ReadRecursive(gstate.CFG_GLOBAL_PREFIX)
	if err != nil {
		return err
	}
	for _, key := range keys {
		tenantId := strings.TrimPrefix(key, gstate.CFG_GLOBAL_PREFIX)
		if !tenantPresent(allCfg, tenantId) {
			opts := *defOpts
			opts.construct.Set(CLI_CONSTRUCT_GLOBAL)
			opts.oper.Set(CLI_OPER_DELETE)
			opts.tenant = tenantId
			log.Printf("deleting tenant %s\n", tenantId)

			err = executeOpts(&opts)
			if err != nil {
				log.Printf("error '%s' deleting tenant %s \n", err, tenantId)
			}
			postProcessing()
		}
	}

	return nil
}

func processAdditions(allCfg *ConfigJson, defOpts *cliOpts) (err error) {
	etcdDriver, err := initEtcd(defOpts)
	if err != nil {
		log.Fatalf("Failed to init etcd driver. Error: %s", err)
	}

	for _, tenant := range allCfg.Tenants {
		addTenant := true
		if defOpts.cfgAdditions && len(tenant.Networks) != 0 {
			gcfg := gstate.Cfg{}
			err = gcfg.Read(etcdDriver, tenant.Name)
			if core.ErrIfKeyExists(err) != nil {
				log.Fatalf("error reading the tenant %s , err '%s'\n",
					tenant, err)
			}
			if err == nil {
				addTenant = false
			}
		}

		if addTenant {
			opts := *defOpts
			opts.construct.Set(CLI_CONSTRUCT_GLOBAL)
			opts.oper.Set(CLI_OPER_CREATE)
			opts.tenant = tenant.Name
			opts.pktTagType = tenant.DefaultNetType
			opts.subnetCidr = tenant.SubnetPool
			opts.allocSubnetLen = tenant.AllocSubnetLen
			opts.vlans = tenant.Vlans
			opts.vxlans = tenant.Vxlans

			log.Printf("creating tenant %s \n", opts.tenant)
			err = executeOpts(&opts)
			if err != nil {
				log.Printf("error pushing global config state: %s \n", err)
				return
			}
		}

		for _, net := range tenant.Networks {

			addNetwork := true
			if defOpts.cfgAdditions && len(tenant.Networks) != 0 {
				nwCfg := &drivers.OvsCfgNetworkState{StateDriver: etcdDriver}
				err = nwCfg.Read(net.Name)
				if core.ErrIfKeyExists(err) != nil {
					log.Fatalf("error reading the net %s , err '%s'\n",
						net, err)
				}
				if err == nil {
					addNetwork = false
				}
			}

			if addNetwork {
				opts = *defOpts
				opts.construct.Set(CLI_CONSTRUCT_NW)
				opts.oper.Set(CLI_OPER_CREATE)
				opts.tenant = tenant.Name
				opts.idStr = net.Name
				if net.PktTag != "" {
					opts.pktTag = net.PktTag
				}
				log.Printf("  creating network %s \n", opts.idStr)
				err = executeOpts(&opts)
				if err != nil {
					log.Printf("error pushing network config state: %s \n", err)
					return
				}
				postProcessing()
			}

			for _, ep := range net.Endpoints {
				opts = *defOpts
				opts.construct.Set(CLI_CONSTRUCT_EP)
				opts.oper.Set(CLI_OPER_CREATE)
				opts.idStr = getEpName(&net, &ep)
				opts.netId = net.Name
				opts.contName = ep.Container
				opts.homingHost = ep.Host
				opts.intfName = ep.Intf
				log.Printf("    creating ep %s \n", opts.idStr)
				err = executeOpts(&opts)
				if err != nil {
					log.Printf("error pushing ep config state: %s \n", err)
					return
				}
				postProcessing()
			}
		}
	}

	return
}

func processDeletions(allCfg *ConfigJson, defOpts *cliOpts) (err error) {
	for _, tenant := range allCfg.Tenants {
		for _, net := range tenant.Networks {
			for _, ep := range net.Endpoints {
				opts = *defOpts
				opts.construct.Set(CLI_CONSTRUCT_EP)
				opts.oper.Set(CLI_OPER_DELETE)
				opts.idStr = getEpName(&net, &ep)
				opts.netId = net.Name
				opts.contName = ep.Container
				opts.homingHost = ep.Host
				opts.intfName = ep.Intf
				log.Printf("deleting ep %s \n", opts.idStr)
				err = executeOpts(&opts)
				if err != nil {
					log.Printf("error pushing ep config state: %s \n", err)
					return
				}
				postProcessing()
			}

			if len(net.Endpoints) == 0 {
				opts = *defOpts
				opts.construct.Set(CLI_CONSTRUCT_NW)
				opts.oper.Set(CLI_OPER_DELETE)
				opts.tenant = tenant.Name
				opts.idStr = net.Name
				log.Printf("deleting network %s \n", opts.idStr)
				err = executeOpts(&opts)
				if err != nil {
					log.Printf("error pushing network config state: %s \n", err)
					return
				}
				postProcessing()
			}
		}

		if len(tenant.Networks) == 0 {
			opts := *defOpts
			opts.construct.Set(CLI_CONSTRUCT_GLOBAL)
			opts.oper.Set(CLI_OPER_DELETE)
			opts.tenant = tenant.Name

			log.Printf("deleting tenant %s \n", opts.tenant)
			err = executeOpts(&opts)
			if err != nil {
				log.Printf("error pushing global config state: %s \n", err)
				return
			}
		}
	}
	return
}

func executeJsonCfg(defOpts *cliOpts) (err error) {
	data, err := ioutil.ReadFile(opts.idStr)
	if err != nil {
		return err
	}

	allCfg := &ConfigJson{}
	err = json.Unmarshal(data, allCfg)
	if err != nil {
		log.Printf("unmarshal error '%s', tenants %v \n", err, allCfg)
		return
	}
	// log.Printf("parsed config %v \n", allCfg)

	if defOpts.cfgDesired {
		err = deleteDelta(allCfg, defOpts)
	}
	if err != nil {
		log.Printf("error deleting delta '%s' \n", err)
		return
	}

	if defOpts.cfgDeletions {
		err = processDeletions(allCfg, defOpts)
	} else {
		err = processAdditions(allCfg, defOpts)
	}
	if err != nil {
		log.Printf("error processing cfg '%s' \n", err)
		return
	}

	return
}
