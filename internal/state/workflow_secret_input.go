package state

// ValidateWorkflowSecretName checks the same name rules used by storage.
func ValidateWorkflowSecretName(name string) error {
	_, err := workflowSecretName(name)
	return err
}

// ValidateWorkflowSecretInput validates without retaining or echoing the value.
func ValidateWorkflowSecretInput(name, value string) error {
	if err := ValidateWorkflowSecretName(name); err != nil {
		return err
	}
	return validateWorkflowSecretValue(value)
}
