/*
 * Licensed to the Apache Software Foundation (ASF) under one or more
 * contributor license agreements.  See the NOTICE file distributed with
 * this work for additional information regarding copyright ownership.
 * The ASF licenses this file to You under the Apache License, Version 2.0
 * (the "License"); you may not use this file except in compliance with
 * the License.  You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package mongo

import (
	"context"
	"fmt"
	"github.com/apache/servicecomb-service-center/pkg/util"
	"strings"

	"github.com/go-chassis/go-chassis/v2/storage"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/apache/servicecomb-service-center/datasource"
	"github.com/apache/servicecomb-service-center/datasource/mongo/client"
	"github.com/apache/servicecomb-service-center/datasource/mongo/client/model"
	"github.com/apache/servicecomb-service-center/datasource/mongo/heartbeat"
	"github.com/apache/servicecomb-service-center/datasource/mongo/sd"
	mutil "github.com/apache/servicecomb-service-center/datasource/mongo/util"
	"github.com/apache/servicecomb-service-center/pkg/log"
	"github.com/apache/servicecomb-service-center/server/config"
)

const defaultExpireTime = 300

func init() {
	datasource.Install("mongo", NewDataSource)
}

type DataSource struct {
	// SchemaEditable determines whether schema modification is allowed for
	SchemaEditable bool
	// TTL options
	ttlFromEnv int64
}

func NewDataSource(opts datasource.Options) (datasource.DataSource, error) {
	// TODO: construct a reasonable DataSource instance

	inst := &DataSource{
		SchemaEditable: opts.SchemaEditable,
		ttlFromEnv:     opts.InstanceTTL,
	}
	// TODO: deal with exception
	if err := inst.initialize(); err != nil {
		return nil, err
	}
	return inst, nil
}

func (ds *DataSource) initialize() error {
	var err error
	// init heartbeat plugins
	err = ds.initPlugins()
	if err != nil {
		return err
	}
	// init mongo client
	err = ds.initClient()
	if err != nil {
		return err
	}
	// create db index and validator
	EnsureDB()
	// init cache
	ds.initStore()
	return nil
}

func (ds *DataSource) initPlugins() error {
	kind := config.GetString("registry.mongo.heartbeat.kind", "cache")
	err := heartbeat.Init(heartbeat.Options{PluginImplName: heartbeat.ImplName(kind)})
	if err != nil {
		log.Fatal("heartbeat init failed", err)
		return err
	}
	return nil
}

func (ds *DataSource) initClient() error {
	uri := config.GetString("registry.mongo.cluster.uri", "mongodb://localhost:27017", config.WithStandby("manager_cluster"))
	sslEnable := config.GetBool("registry.mongo.cluster.sslEnabled", false)
	rootCA := config.GetString("registry.mongo.cluster.rootCAFile", "/opt/ssl/ca.crt")
	verifyPeer := config.GetBool("registry.mongo.cluster.verifyPeer", false)
	certFile := config.GetString("registry.mongo.cluster.certFile", "")
	keyFile := config.GetString("registry.mongo.cluster.keyFile", "")
	cfg := storage.NewConfig(uri, storage.SSLEnabled(sslEnable), storage.RootCA(rootCA), storage.VerifyPeer(verifyPeer), storage.CertFile(certFile), storage.KeyFile(keyFile))
	client.NewMongoClient(cfg)
	select {
	case err := <-client.GetMongoClient().Err():
		return err
	case <-client.GetMongoClient().Ready():
		return nil
	}
}

func EnsureDB() {
	EnsureService()
	EnsureInstance()
	EnsureRule()
	EnsureSchema()
	EnsureDep()
}

func EnsureService() {
	err := client.GetMongoClient().GetDB().CreateCollection(context.Background(), model.CollectionService, options.CreateCollection().SetValidator(nil))
	wrapCreateCollectionError(err)

	serviceIDIndex := mutil.BuildIndexDoc(
		mutil.ConnectWithDot([]string{model.ColumnService, model.ColumnServiceID}))
	serviceIDIndex.Options = options.Index().SetUnique(true)

	serviceIndex := mutil.BuildIndexDoc(
		mutil.ConnectWithDot([]string{model.ColumnService, model.ColumnAppID}),
		mutil.ConnectWithDot([]string{model.ColumnService, model.ColumnServiceName}),
		mutil.ConnectWithDot([]string{model.ColumnService, model.ColumnEnv}),
		mutil.ConnectWithDot([]string{model.ColumnService, model.ColumnVersion}),
		model.ColumnDomain,
		model.ColumnProject)
	serviceIndex.Options = options.Index().SetUnique(true)

	var serviceIndexs []mongo.IndexModel
	serviceIndexs = append(serviceIndexs, serviceIDIndex, serviceIndex)

	err = client.GetMongoClient().CreateIndexes(context.Background(), model.CollectionService, serviceIndexs)
	wrapCreateIndexesError(err)
}

func EnsureInstance() {
	err := client.GetMongoClient().GetDB().CreateCollection(context.Background(), model.CollectionInstance, options.CreateCollection().SetValidator(nil))
	wrapCreateCollectionError(err)

	instanceIndex := mutil.BuildIndexDoc(model.ColumnRefreshTime)
	instanceIndex.Options = options.Index().SetExpireAfterSeconds(defaultExpireTime)

	instanceServiceIndex := mutil.BuildIndexDoc(mutil.ConnectWithDot([]string{model.ColumnInstance, model.ColumnServiceID}))

	var instanceIndexs []mongo.IndexModel
	instanceIndexs = append(instanceIndexs, instanceIndex, instanceServiceIndex)

	err = client.GetMongoClient().CreateIndexes(context.Background(), model.CollectionInstance, instanceIndexs)
	wrapCreateIndexesError(err)
}

func EnsureSchema() {
	err := client.GetMongoClient().GetDB().CreateCollection(context.Background(), model.CollectionSchema, options.CreateCollection().SetValidator(nil))
	wrapCreateCollectionError(err)

	schemaServiceIndex := mutil.BuildIndexDoc(
		model.ColumnDomain,
		model.ColumnProject,
		model.ColumnServiceID)

	var schemaIndexs []mongo.IndexModel
	schemaIndexs = append(schemaIndexs, schemaServiceIndex)

	err = client.GetMongoClient().CreateIndexes(context.Background(), model.CollectionSchema, schemaIndexs)
	wrapCreateIndexesError(err)
}

func EnsureRule() {
	err := client.GetMongoClient().GetDB().CreateCollection(context.Background(), model.CollectionRule, options.CreateCollection().SetValidator(nil))
	wrapCreateCollectionError(err)

	ruleServiceIndex := mutil.BuildIndexDoc(
		model.ColumnDomain,
		model.ColumnProject,
		model.ColumnServiceID)

	var ruleIndexs []mongo.IndexModel
	ruleIndexs = append(ruleIndexs, ruleServiceIndex)

	err = client.GetMongoClient().CreateIndexes(context.Background(), model.CollectionRule, ruleIndexs)
	wrapCreateIndexesError(err)
}

func EnsureDep() {
	err := client.GetMongoClient().GetDB().CreateCollection(context.Background(), model.CollectionDep, options.CreateCollection().SetValidator(nil))
	wrapCreateCollectionError(err)

	depServiceIndex := mutil.BuildIndexDoc(
		model.ColumnDomain,
		model.ColumnProject,
		model.ColumnServiceKey)

	var depIndexs []mongo.IndexModel
	depIndexs = append(depIndexs, depServiceIndex)

	err = client.GetMongoClient().CreateIndexes(context.Background(), model.CollectionDep, depIndexs)
	if err != nil {
		log.Fatal("failed to create dep collection indexs", err)
		return
	}
}

func wrapCreateCollectionError(err error) {
	if err != nil {
		if strings.Contains(err.Error(), client.MsgDBExists) {
			log.Info(fmt.Sprintf("failed to create collection, %s", err))
			return
		}
		log.Fatal(fmt.Sprintf("failed to create collection with validation, err type: %s", util.Reflect(err).FullName), err)
	}
}

func wrapCreateIndexesError(err error) {
	if err != nil {
		if strings.Contains(err.Error(), client.MsgDuplicate) {
			log.Info(fmt.Sprintf("failed to create indexes, %s", err))
			return
		}
		log.Fatal(fmt.Sprintf("failed to create indexes, err type: %s", util.Reflect(err).FullName), err)
	}
}

func (ds *DataSource) initStore() {
	if !config.GetRegistry().EnableCache {
		log.Debug("cache is disabled")
		return
	}
	sd.Store().Run()
	<-sd.Store().Ready()
}
