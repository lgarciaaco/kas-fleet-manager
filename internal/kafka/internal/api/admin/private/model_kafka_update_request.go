/*
 * Kafka Service Fleet Manager Admin APIs
 *
 * The admin APIs for the fleet manager of Kafka service
 *
 * API version: 0.1.0
 * Contact: rhosak-support@redhat.com
 * Generated by: OpenAPI Generator (https://openapi-generator.tech)
 */

package private

// KafkaUpdateRequest struct for KafkaUpdateRequest
type KafkaUpdateRequest struct {
	StrimziVersion  string `json:"strimzi_version,omitempty"`
	KafkaVersion    string `json:"kafka_version,omitempty"`
	KafkaIbpVersion string `json:"kafka_ibp_version,omitempty"`
	// Maximum data storage available to this Kafka
	MaxDataRetentionSize string `json:"max_data_retention_size,omitempty"`
	// boolean value indicating whether kafka should be suspended or not depending on the value provided. Suspended kafkas have their certain resources removed and become inaccessible until fully unsuspended (restored to Ready state).
	Suspended *bool `json:"suspended,omitempty"`
}
