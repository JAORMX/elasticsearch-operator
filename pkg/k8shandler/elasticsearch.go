package k8shandler

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	api "github.com/openshift/elasticsearch-operator/pkg/apis/logging/v1"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	certLocalPath = "/tmp/"
)

type esCurlStruct struct {
	Method       string // use net/http constants https://golang.org/pkg/net/http/#pkg-constants
	URI          string
	RequestBody  string
	StatusCode   int
	ResponseBody map[string]interface{}
	Error        error
}

func SetShardAllocation(clusterName, namespace string, state api.ShardAllocationState, client client.Client) (bool, error) {

	payload := &esCurlStruct{
		Method:      http.MethodPut,
		URI:         "_cluster/settings",
		RequestBody: fmt.Sprintf("{%q:{%q:%q}}", "transient", "cluster.routing.allocation.enable", state),
	}

	curlESService(clusterName, namespace, payload, client)

	acknowledged := false
	if acknowledgedBool, ok := payload.ResponseBody["acknowledged"].(bool); ok {
		acknowledged = acknowledgedBool
	}
	return (payload.StatusCode == 200 && acknowledged), payload.Error
}

func GetShardAllocation(clusterName, namespace string, client client.Client) (string, error) {

	payload := &esCurlStruct{
		Method: http.MethodGet,
		URI:    "_cluster/settings",
	}

	curlESService(clusterName, namespace, payload, client)

	allocation := parseString("transient.cluster.routing.allocation.enable", payload.ResponseBody)

	return allocation, payload.Error
}

func GetNodeDiskUsage(clusterName, namespace, nodeName string, client client.Client) (string, float64, error) {

	payload := &esCurlStruct{
		Method: http.MethodGet,
		URI:    "_cat/nodes?h=name,du,dup",
	}

	curlESService(clusterName, namespace, payload, client)

	usage := ""
	percentUsage := float64(-1)

	if payload, ok := payload.ResponseBody["results"].(string); ok {
		response := parseNodeDiskUsage(payload)
		if nodeResponse, ok := response[nodeName].(map[string]interface{}); ok {
			if usageString, ok := nodeResponse["used"].(string); ok {
				usage = usageString
			}

			if percentUsageFloat, ok := nodeResponse["used_percent"].(float64); ok {
				percentUsage = percentUsageFloat
			}
		}
	}

	return usage, percentUsage, payload.Error
}

func GetThresholdEnabled(clusterName, namespace string, client client.Client) (bool, error) {

	payload := &esCurlStruct{
		Method: http.MethodGet,
		URI:    "_cluster/settings?include_defaults=true",
	}

	curlESService(clusterName, namespace, payload, client)

	var enabled interface{}

	if value := walkInterfaceMap(
		"defaults.cluster.routing.allocation.disk.threshold_enabled",
		payload.ResponseBody); value != nil {

		enabled = value
	}

	if value := walkInterfaceMap(
		"persistent.cluster.routing.allocation.disk.threshold_enabled",
		payload.ResponseBody); value != nil {

		enabled = value
	}

	if value := walkInterfaceMap(
		"transient.cluster.routing.allocation.disk.threshold_enabled",
		payload.ResponseBody); value != nil {

		enabled = value
	}

	enabledBool := false
	if enabledString, ok := enabled.(string); ok {
		if enabledTemp, err := strconv.ParseBool(enabledString); err == nil {
			enabledBool = enabledTemp
		}
	}

	return enabledBool, payload.Error
}

func GetDiskWatermarks(clusterName, namespace string, client client.Client) (interface{}, interface{}, error) {

	payload := &esCurlStruct{
		Method: http.MethodGet,
		URI:    "_cluster/settings?include_defaults=true",
	}

	curlESService(clusterName, namespace, payload, client)

	var low interface{}
	var high interface{}

	if value := walkInterfaceMap(
		"defaults.cluster.routing.allocation.disk.watermark.low",
		payload.ResponseBody); value != nil {

		low = value
	}

	if value := walkInterfaceMap(
		"defaults.cluster.routing.allocation.disk.watermark.high",
		payload.ResponseBody); value != nil {

		high = value
	}

	if value := walkInterfaceMap(
		"persistent.cluster.routing.allocation.disk.watermark.low",
		payload.ResponseBody); value != nil {

		low = value
	}

	if value := walkInterfaceMap(
		"persistent.cluster.routing.allocation.disk.watermark.high",
		payload.ResponseBody); value != nil {

		high = value
	}

	if value := walkInterfaceMap(
		"transient.cluster.routing.allocation.disk.watermark.low",
		payload.ResponseBody); value != nil {

		low = value
	}

	if value := walkInterfaceMap(
		"transient.cluster.routing.allocation.disk.watermark.high",
		payload.ResponseBody); value != nil {

		high = value
	}

	if lowString, ok := low.(string); ok {
		if strings.HasSuffix(lowString, "%") {
			low, _ = strconv.ParseFloat(strings.TrimSuffix(lowString, "%"), 64)
		} else {
			if strings.HasSuffix(lowString, "b") {
				low = strings.TrimSuffix(lowString, "b")
			}
		}
	}

	if highString, ok := high.(string); ok {
		if strings.HasSuffix(highString, "%") {
			high, _ = strconv.ParseFloat(strings.TrimSuffix(highString, "%"), 64)
		} else {
			if strings.HasSuffix(highString, "b") {
				high = strings.TrimSuffix(highString, "b")
			}
		}
	}

	return low, high, payload.Error
}

func parseBool(path string, interfaceMap map[string]interface{}) bool {
	value := walkInterfaceMap(path, interfaceMap)

	if parsedBool, ok := value.(bool); ok {
		return parsedBool
	} else {
		return false
	}
}

func parseString(path string, interfaceMap map[string]interface{}) string {
	value := walkInterfaceMap(path, interfaceMap)

	if parsedString, ok := value.(string); ok {
		return parsedString
	} else {
		return ""
	}
}

func parseInt32(path string, interfaceMap map[string]interface{}) int32 {
	return int32(parseFloat64(path, interfaceMap))
}

func parseFloat64(path string, interfaceMap map[string]interface{}) float64 {
	value := walkInterfaceMap(path, interfaceMap)

	if parsedFloat, ok := value.(float64); ok {
		return parsedFloat
	} else {
		return float64(-1)
	}
}

func walkInterfaceMap(path string, interfaceMap map[string]interface{}) interface{} {

	current := interfaceMap
	keys := strings.Split(path, ".")
	keyCount := len(keys)

	for index, key := range keys {
		if current[key] != nil {
			if index+1 < keyCount {
				current = current[key].(map[string]interface{})
			} else {
				return current[key]
			}
		} else {
			return nil
		}
	}

	return nil
}

// ---
// method: GET
// uri: _cat/nodes?h=name,du,dup
// requestbody: ""
// statuscode: 200
// responsebody:
//   results: |
//     elasticsearch-cm-23bq83d3   6.3gb 5.36
//     elasticsearch-cd-ujt4y3n5-1 6.4gb 5.43
// error: null
func parseNodeDiskUsage(results string) map[string]interface{} {

	nodeDiskUsage := make(map[string]interface{})

	for _, result := range strings.Split(results, "\n") {

		fields := []string{}
		for _, val := range strings.Split(result, " ") {
			if len(val) > 0 {
				fields = append(fields, val)
			}
		}

		if len(fields) == 3 {
			percent, err := strconv.ParseFloat(fields[2], 64)
			if err != nil {
				percent = float64(-1)
			}

			nodeDiskUsage[fields[0]] = map[string]interface{}{
				"used":         strings.ToUpper(strings.TrimSuffix(fields[1], "b")),
				"used_percent": percent,
			}
		}
	}

	return nodeDiskUsage
}

func SetMinMasterNodes(clusterName, namespace string, numberMasters int32, client client.Client) (bool, error) {

	payload := &esCurlStruct{
		Method:      http.MethodPut,
		URI:         "_cluster/settings",
		RequestBody: fmt.Sprintf("{%q:{%q:%d}}", "persistent", "discovery.zen.minimum_master_nodes", numberMasters),
	}

	curlESService(clusterName, namespace, payload, client)

	acknowledged := false
	if acknowledgedBool, ok := payload.ResponseBody["acknowledged"].(bool); ok {
		acknowledged = acknowledgedBool
	}

	return (payload.StatusCode == 200 && acknowledged), payload.Error
}

func GetMinMasterNodes(clusterName, namespace string, client client.Client) (int32, error) {

	payload := &esCurlStruct{
		Method: http.MethodGet,
		URI:    "_cluster/settings",
	}

	curlESService(clusterName, namespace, payload, client)

	masterCount := int32(0)
	if payload.ResponseBody["persistent"] != nil {
		persistentBody := payload.ResponseBody["persistent"].(map[string]interface{})
		if masterCountFloat, ok := persistentBody["discovery.zen.minimum_master_nodes"].(float64); ok {
			masterCount = int32(masterCountFloat)
		}
	}

	return masterCount, payload.Error
}

func GetClusterHealth(clusterName, namespace string, client client.Client) (api.ClusterHealth, error) {

	clusterHealth := api.ClusterHealth{}

	payload := &esCurlStruct{
		Method: http.MethodGet,
		URI:    "_cluster/health",
	}

	curlESService(clusterName, namespace, payload, client)

	if payload.Error != nil {
		return clusterHealth, payload.Error
	}

	clusterHealth.Status = parseString("status", payload.ResponseBody)
	clusterHealth.NumNodes = parseInt32("number_of_nodes", payload.ResponseBody)
	clusterHealth.NumDataNodes = parseInt32("number_of_data_nodes", payload.ResponseBody)
	clusterHealth.ActivePrimaryShards = parseInt32("active_primary_shards", payload.ResponseBody)
	clusterHealth.ActiveShards = parseInt32("active_shards", payload.ResponseBody)
	clusterHealth.RelocatingShards = parseInt32("relocating_shards", payload.ResponseBody)
	clusterHealth.InitializingShards = parseInt32("initializing_shards", payload.ResponseBody)
	clusterHealth.UnassignedShards = parseInt32("unassigned_shards", payload.ResponseBody)
	clusterHealth.PendingTasks = parseInt32("number_of_pending_tasks", payload.ResponseBody)

	return clusterHealth, nil
}

func GetClusterHealthStatus(clusterName, namespace string, client client.Client) (string, error) {

	payload := &esCurlStruct{
		Method: http.MethodGet,
		URI:    "_cluster/health",
	}

	curlESService(clusterName, namespace, payload, client)

	status := ""
	if payload.ResponseBody["status"] != nil {
		if statusString, ok := payload.ResponseBody["status"].(string); ok {
			status = statusString
		}
	}

	return status, payload.Error
}

func GetClusterNodeCount(clusterName, namespace string, client client.Client) (int32, error) {

	payload := &esCurlStruct{
		Method: http.MethodGet,
		URI:    "_cluster/health",
	}

	curlESService(clusterName, namespace, payload, client)

	nodeCount := int32(0)
	if nodeCountFloat, ok := payload.ResponseBody["number_of_nodes"].(float64); ok {
		// we expect at most double digit numbers here, eg cluster with 15 nodes
		nodeCount = int32(nodeCountFloat)
	}

	return nodeCount, payload.Error
}

// TODO: also check that the number of shards in the response > 0?
func DoSynchronizedFlush(clusterName, namespace string, client client.Client) (bool, error) {

	payload := &esCurlStruct{
		Method: http.MethodPost,
		URI:    "_flush/synced",
	}

	curlESService(clusterName, namespace, payload, client)

	failed := 0
	if shards, ok := payload.ResponseBody["_shards"].(map[string]interface{}); ok {
		if failedFload, ok := shards["failed"].(float64); ok {
			failed = int(failedFload)
		}
	}

	if payload.Error == nil && failed != 0 {
		payload.Error = fmt.Errorf("Failed to flush %d shards in preparation for cluster restart", failed)
	}

	return (payload.StatusCode == 200), payload.Error
}

// This will idempompotently update the index templates and update indices' replica count
func UpdateReplicaCount(clusterName, namespace string, client client.Client, replicaCount int32) (bool, error) {

	if ok, _ := updateAllIndexTemplateReplicas(clusterName, namespace, client, replicaCount); ok {
		if ok, _ = updateAllIndexReplicas(clusterName, namespace, client, replicaCount); ok {
			return true, nil
		}
	}

	return false, nil
}

func updateAllIndexReplicas(clusterName, namespace string, client client.Client, replicaCount int32) (bool, error) {

	indexHealth, _ := getIndexHealth(clusterName, namespace, client)

	// get list of indices and call updateIndexReplicas for each one
	for index, health := range indexHealth {
		// only update replicas for indices that don't have same replica count
		if parseInt32("replicas", health.(map[string]interface{})) != replicaCount {
			// best effort initially?
			logrus.Debugf("Updating %v from %d replicas to %d", index, parseInt32("replicas", health.(map[string]interface{})), replicaCount)
			updateIndexReplicas(clusterName, namespace, client, index, replicaCount)
		}
	}

	return true, nil
}

func getIndexHealth(clusterName, namespace string, client client.Client) (map[string]interface{}, error) {
	payload := &esCurlStruct{
		Method: http.MethodGet,
		URI:    "_cat/indices?h=health,status,index,pri,rep",
	}

	curlESService(clusterName, namespace, payload, client)

	response := make(map[string]interface{})
	if payload, ok := payload.ResponseBody["results"].(string); ok {
		response = parseIndexHealth(payload)
	}

	return response, payload.Error
}

// ---
// method: GET
// uri: _cat/indices?h=health,status,index,pri,rep
// requestbody: ""
// statuscode: 200
// responsebody:
//   results: |
//	 	green open .searchguard           1 0
//		green open .kibana                1 0
//		green open .operations.2019.07.01 1 0
// error: null
func parseIndexHealth(results string) map[string]interface{} {

	indexHealth := make(map[string]interface{})

	for _, result := range strings.Split(results, "\n") {

		fields := []string{}
		for _, val := range strings.Split(result, " ") {
			if len(val) > 0 {
				fields = append(fields, val)
			}
		}

		if len(fields) == 5 {
			primary, err := strconv.ParseFloat(fields[3], 64)
			if err != nil {
				primary = float64(-1)
			}
			replicas, err := strconv.ParseFloat(fields[4], 64)
			if err != nil {
				replicas = float64(-1)
			}

			indexHealth[fields[2]] = map[string]interface{}{
				"health":   fields[0],
				"status":   fields[1],
				"primary":  primary,
				"replicas": replicas,
			}
		}
	}

	return indexHealth
}

func updateAllIndexTemplateReplicas(clusterName, namespace string, client client.Client, replicaCount int32) (bool, error) {

	// get list of all common.* index templates and update their replica count for each one
	payload := &esCurlStruct{
		Method: http.MethodGet,
		URI:    "_cat/templates/common.*",
	}

	curlESService(clusterName, namespace, payload, client)

	commonTemplates := []string{}
	if payload, ok := payload.ResponseBody["results"].(string); ok {
		for _, result := range strings.Split(payload, "\n") {

			fields := []string{}
			for _, val := range strings.Split(result, " ") {
				if len(val) > 0 {
					fields = append(fields, val)
				}
			}

			if len(fields) == 1 {
				commonTemplates = append(commonTemplates, fields[0])
			}
		}
	}

	for _, template := range commonTemplates {
		updateIndexTemplateReplicas(clusterName, namespace, client, template, replicaCount)
	}

	return true, nil
}

func updateIndexTemplateReplicas(clusterName, namespace string, client client.Client, templateName string, replicaCount int32) (bool, error) {

	// get the index template and then update the replica and put it
	payload := &esCurlStruct{
		Method: http.MethodGet,
		URI:    fmt.Sprintf("_template/%s", templateName),
	}

	curlESService(clusterName, namespace, payload, client)

	if template, ok := payload.ResponseBody[templateName].(map[string]interface{}); ok {
		if settings, ok := template["settings"].(map[string]interface{}); ok {
			if index, ok := settings["index"].(map[string]interface{}); ok {
				currentReplicas, ok := index["number_of_replicas"].(string)
				if ok && currentReplicas != fmt.Sprintf("%d", replicaCount) {
					template["settings"].(map[string]interface{})["index"].(map[string]interface{})["number_of_replicas"] = fmt.Sprintf("%d", replicaCount)

					templateJson, _ := json.Marshal(template)

					logrus.Debugf("Updating template %v from %d replicas to %d", templateName, currentReplicas, replicaCount)

					payload = &esCurlStruct{
						Method:      http.MethodPut,
						URI:         fmt.Sprintf("_template/%s", templateName),
						RequestBody: string(templateJson),
					}

					curlESService(clusterName, namespace, payload, client)

					acknowledged := false
					if acknowledgedBool, ok := payload.ResponseBody["acknowledged"].(bool); ok {
						acknowledged = acknowledgedBool
					}
					return (payload.StatusCode == 200 && acknowledged), payload.Error
				}
			}
		}
	}

	return false, payload.Error
}

func updateIndexReplicas(clusterName, namespace string, client client.Client, index string, replicaCount int32) (bool, error) {
	payload := &esCurlStruct{
		Method:      http.MethodPut,
		URI:         fmt.Sprintf("%s/_settings", index),
		RequestBody: fmt.Sprintf("{%q:\"%d\"}}", "index.number_of_replicas", replicaCount),
	}

	curlESService(clusterName, namespace, payload, client)

	acknowledged := false
	if acknowledgedBool, ok := payload.ResponseBody["acknowledged"].(bool); ok {
		acknowledged = acknowledgedBool
	}
	return (payload.StatusCode == 200 && acknowledged), payload.Error
}

func ensureTokenHeader(header http.Header) http.Header {

	if header == nil {
		header = map[string][]string{}
	}

	if saToken, ok := readSAToken(k8sTokenFile); ok {
		header["x-forwarded-access-token"] = []string{
			saToken,
		}
	}

	return header
}

// we want to read each time so that we can be sure to have the most up to date
// token in the case where our perms change and a new token is mounted
func readSAToken(tokenFile string) (string, bool) {
	// read from /var/run/secrets/kubernetes.io/serviceaccount/token
	token, err := ioutil.ReadFile(tokenFile)

	if err != nil {
		logrus.Errorf("Unable to read auth token from file [%s]: %v", tokenFile, err)
		return "", false
	}

	if len(token) == 0 {
		logrus.Errorf("Unable to read auth token from file [%s]: empty token", tokenFile)
		return "", false
	}

	return string(token), true
}

// This will curl the ES service and provide the certs required for doing so
//  it will also return the http and string response
func curlESService(clusterName, namespace string, payload *esCurlStruct, client client.Client) {

	urlString := fmt.Sprintf("https://%s.%s.svc:9200/%s", clusterName, namespace, payload.URI)
	urlURL, err := url.Parse(urlString)

	if err != nil {
		logrus.Warnf("Unable to parse URL %v: %v", urlString, err)
		return
	}

	request := &http.Request{
		Method: payload.Method,
		URL:    urlURL,
	}

	switch payload.Method {
	case http.MethodGet:
		// no more to do to request...
	case http.MethodPost:
		if payload.RequestBody != "" {
			// add to the request
			request.Header = map[string][]string{
				"Content-Type": []string{
					"application/json",
				},
			}
			request.Body = ioutil.NopCloser(bytes.NewReader([]byte(payload.RequestBody)))
		}

	case http.MethodPut:
		if payload.RequestBody != "" {
			// add to the request
			request.Header = map[string][]string{
				"Content-Type": []string{
					"application/json",
				},
			}
			request.Body = ioutil.NopCloser(bytes.NewReader([]byte(payload.RequestBody)))
		}

	default:
		// unsupported method -- do nothing
		return
	}

	request.Header = ensureTokenHeader(request.Header)
	httpClient := getClient(clusterName, namespace, client)
	resp, err := httpClient.Do(request)

	if resp != nil {
		// TODO: eventually remove after all ES images have been updated to use SA token auth for EO?
		if resp.StatusCode == http.StatusForbidden ||
			resp.StatusCode == http.StatusUnauthorized {
			// if we get a 401 that means that we couldn't read from the token and provided
			// no header.
			// if we get a 403 that means the ES cluster doesn't allow us to use
			// our SA token.
			// in both cases, try the old way.

			// Not sure why, but just trying to reuse the request with the old client
			// resulted in a 400 every time. Doing it this way got a 200 response as expected.
			curlESServiceOldClient(clusterName, namespace, payload, client)
			return
		}

		payload.StatusCode = resp.StatusCode
		payload.ResponseBody = getMapFromBody(resp.Body)
	}

	payload.Error = err
}

func curlESServiceOldClient(clusterName, namespace string, payload *esCurlStruct, client client.Client) {

	urlString := fmt.Sprintf("https://%s.%s.svc:9200/%s", clusterName, namespace, payload.URI)
	urlURL, err := url.Parse(urlString)

	if err != nil {
		logrus.Warnf("Unable to parse URL %v: %v", urlString, err)
		return
	}

	request := &http.Request{
		Method: payload.Method,
		URL:    urlURL,
	}

	switch payload.Method {
	case http.MethodGet:
		// no more to do to request...
	case http.MethodPost:
		if payload.RequestBody != "" {
			// add to the request
			request.Header = map[string][]string{
				"Content-Type": []string{
					"application/json",
				},
			}
			request.Body = ioutil.NopCloser(bytes.NewReader([]byte(payload.RequestBody)))
		}

	case http.MethodPut:
		if payload.RequestBody != "" {
			// add to the request
			request.Header = map[string][]string{
				"Content-Type": []string{
					"application/json",
				},
			}
			request.Body = ioutil.NopCloser(bytes.NewReader([]byte(payload.RequestBody)))
		}

	default:
		// unsupported method -- do nothing
		return
	}

	httpClient := getOldClient(clusterName, namespace, client)
	resp, err := httpClient.Do(request)

	if resp != nil {
		payload.StatusCode = resp.StatusCode
		payload.ResponseBody = getMapFromBody(resp.Body)
	}

	payload.Error = err
}

func getRootCA(clusterName, namespace string) *x509.CertPool {
	certPool := x509.NewCertPool()

	// load cert into []byte
	caPem, err := ioutil.ReadFile(path.Join(certLocalPath, clusterName, "admin-ca"))
	if err != nil {
		logrus.Errorf("Unable to read file to get contents: %v", err)
		return nil
	}

	certPool.AppendCertsFromPEM(caPem)

	return certPool
}

func getMapFromBody(body io.ReadCloser) map[string]interface{} {
	buf := new(bytes.Buffer)
	buf.ReadFrom(body)

	var results map[string]interface{}
	err := json.Unmarshal([]byte(buf.String()), &results)
	if err != nil {
		results = make(map[string]interface{})
		results["results"] = buf.String()
	}

	return results
}

func getClientCertificates(clusterName, namespace string) []tls.Certificate {
	certificate, err := tls.LoadX509KeyPair(
		path.Join(certLocalPath, clusterName, "admin-cert"),
		path.Join(certLocalPath, clusterName, "admin-key"),
	)
	if err != nil {
		return []tls.Certificate{}
	}

	return []tls.Certificate{
		certificate,
	}
}

func getClient(clusterName, namespace string, client client.Client) *http.Client {

	// http.Transport sourced from go 1.10.7
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
				DualStack: true,
			}).DialContext,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			// we cannot rely on certificates as they may rotate and therefore would be invalid
			// since ES listens on https and presents a server cert, we need to not verify it
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}
}

func getOldClient(clusterName, namespace string, client client.Client) *http.Client {

	// get the contents of the secret
	extractSecret(clusterName, namespace, client)

	// http.Transport sourced from go 1.10.7
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
				DualStack: true,
			}).DialContext,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: false,
				RootCAs:            getRootCA(clusterName, namespace),
				Certificates:       getClientCertificates(clusterName, namespace),
			},
		},
	}
}

func extractSecret(secretName, namespace string, client client.Client) {
	secret := &v1.Secret{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Secret",
			APIVersion: v1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: namespace,
		},
	}
	if err := client.Get(context.TODO(), types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, secret); err != nil {
		if errors.IsNotFound(err) {
			//return err
			logrus.Errorf("Unable to find secret %v: %v", secretName, err)
		}

		logrus.Errorf("Error reading secret %v: %v", secretName, err)
		//return fmt.Errorf("Unable to extract secret to file: %v", secretName, err)
	}

	// make sure that the dir === secretName exists
	if _, err := os.Stat(path.Join(certLocalPath, secretName)); os.IsNotExist(err) {
		err = os.MkdirAll(path.Join(certLocalPath, secretName), 0755)
		if err != nil {
			logrus.Errorf("Error creating dir %v: %v", path.Join(certLocalPath, secretName), err)
		}
	}

	for _, key := range []string{"admin-ca", "admin-cert", "admin-key"} {

		value, ok := secret.Data[key]

		// check to see if the map value exists
		if !ok {
			logrus.Errorf("Error secret key %v not found", key)
			//return fmt.Errorf("No secret data \"%s\" found", key)
		}

		if err := ioutil.WriteFile(path.Join(certLocalPath, secretName, key), value, 0644); err != nil {
			//return fmt.Errorf("Unable to write to working dir: %v", err)
			logrus.Errorf("Error writing %v to %v: %v", value, path.Join(certLocalPath, secretName, key), err)
		}
	}
}
