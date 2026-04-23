package ul

type PDUType byte

const (
	PDUAssociateRQ PDUType = 0x01
	PDUAssociateAC PDUType = 0x02
	PDUAssociateRJ PDUType = 0x03
	PDUDataTF      PDUType = 0x04
	PDUReleaseRQ   PDUType = 0x05
	PDUReleaseRP   PDUType = 0x06
	PDUAbort       PDUType = 0x07
)

type PDU struct {
	Type PDUType
	Data []byte
}

type PresentationContext struct {
	ID                 byte
	AbstractSyntaxUID  string
	TransferSyntaxUIDs []string
}
