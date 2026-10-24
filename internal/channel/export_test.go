package channel

// NewReplySenderForTest exposes the manager's reply sender to the external
// contract tests, which import the adapter packages and so cannot live in
// package channel.
func NewReplySenderForTest(m *Manager, cfg ChannelConfig, channelType ChannelType) StreamReplySender {
	return m.newReplySender(cfg, channelType)
}
