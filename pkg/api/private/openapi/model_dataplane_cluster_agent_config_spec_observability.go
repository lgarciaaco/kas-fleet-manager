/*
 * Kafka Service Fleet Manager
 *
 * Kafka Service Fleet Manager is a Rest API to manage kafka instances and connectors.
 *
 * API version: 0.0.1
 * Generated by: OpenAPI Generator (https://openapi-generator.tech)
 */

package openapi

// DataplaneClusterAgentConfigSpecObservability Observability configurations
type DataplaneClusterAgentConfigSpecObservability struct {
	AccessToken string `json:"accessToken,omitempty"`
	Channel     string `json:"channel,omitempty"`
	Repository  string `json:"repository,omitempty"`
	Tag         string `json:"tag,omitempty"`
}