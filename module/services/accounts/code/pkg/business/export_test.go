package business

import "accounts/pkg/datasource/connector"

// ConnectorSource is the envelope's view of a stored source, as the host hands
// it to a registered connector.
func ConnectorSource(source *DatasourceSource) connector.Source { return connectorSource(source) }
