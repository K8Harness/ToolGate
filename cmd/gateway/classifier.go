package main

import "strings"

type OperationClass int

const (
	OperationClassRead OperationClass = iota
	OperationClassWrite
)

type OperationClassifier struct {
	classes map[string]OperationClass
}

func NewOperationClassifier(classes map[string]string) *OperationClassifier {
	classifier := &OperationClassifier{
		classes: make(map[string]OperationClass, len(classes)),
	}

	for toolName, class := range classes {
		if class == "read" {
			classifier.classes[toolName] = OperationClassRead
			continue
		}
		if class == "write" {
			classifier.classes[toolName] = OperationClassWrite
		}
	}

	return classifier
}

func (c *OperationClassifier) Classify(toolName string) OperationClass {
	if c != nil {
		if class, ok := c.classes[toolName]; ok {
			return class
		}
	}

	if strings.HasPrefix(toolName, "read_") ||
		strings.HasPrefix(toolName, "get_") ||
		strings.HasPrefix(toolName, "list_") {
		return OperationClassRead
	}

	return OperationClassWrite
}
