package accountconfig

import "path/filepath"

const (
	FileName           = "config.yaml"
	ContainerDirectory = "/CLIProxyAPI/account-config"
	ContainerFile      = ContainerDirectory + "/" + FileName
)

func Directory(root, accountID string) string {
	return filepath.Join(root, "configs", accountID)
}

func File(root, accountID string) string {
	return filepath.Join(Directory(root, accountID), FileName)
}

// LegacyFile is the pre-directory layout retained only while a running
// container still has the single file bind-mounted by inode.
func LegacyFile(root, accountID string) string {
	return filepath.Join(root, "configs", accountID+".yaml")
}
