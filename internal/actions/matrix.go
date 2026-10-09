package actions

import (
	"fmt"
	"reflect"
	"sort"
)

const MaxJobs = 16

// ExpandMatrix retains axis order, then applies exclude and include. limit is
// the remaining capacity of this workflow, not a per-job allowance.
func ExpandMatrix(matrix map[string]any, order []string, limit int) ([]map[string]any, error) {
	if limit < 0 || limit > MaxJobs {
		limit = MaxJobs
	}
	if _, err := checkedValue(matrix); err != nil {
		return nil, err
	}
	axes := make(map[string][]any)
	for key, value := range matrix {
		if key == "include" || key == "exclude" {
			continue
		}
		if !validIdentifier(key) {
			return nil, expressionError("strategy.matrix."+key, "a matrix identifier")
		}
		values, ok := value.([]any)
		if !ok || len(values) == 0 {
			return nil, expressionError("strategy.matrix."+key, "a nonempty list of scalars or mappings")
		}
		for _, value := range values {
			if _, nested := value.([]any); nested {
				return nil, expressionError("strategy.matrix."+key, "scalars or mappings, not nested lists")
			}
		}
		axes[key] = values
	}
	if len(order) == 0 {
		order = sortedKeys(axes)
	}
	if len(order) != len(axes) {
		return nil, expressionError("strategy.matrix", "each axis exactly once in axis order")
	}
	seen := map[string]bool{}
	for _, key := range order {
		if _, ok := axes[key]; !ok || seen[key] {
			return nil, expressionError("strategy.matrix", "each axis exactly once in axis order")
		}
		seen[key] = true
	}
	include, err := matrixObjects(matrix["include"], "include")
	if err != nil {
		return nil, err
	}
	exclude, err := matrixObjects(matrix["exclude"], "exclude")
	if err != nil {
		return nil, err
	}
	for _, item := range exclude {
		for key := range item {
			if _, ok := axes[key]; !ok {
				return nil, expressionError("strategy.matrix.exclude", "only declared axis keys")
			}
		}
	}
	var originals, jobs []map[string]any
	steps := 0
	var expand func(int, map[string]any) error
	expand = func(index int, combination map[string]any) error {
		steps++
		if steps > MaxEvaluationSteps {
			return limitError("matrix expansion steps", MaxEvaluationSteps)
		}
		if isExcludedSubtree(combination, exclude) {
			return nil
		}
		if index == len(order) {
			if len(jobs) >= limit {
				return tooManyJobs(limit + 1)
			}
			originals = append(originals, cloneMap(combination))
			jobs = append(jobs, cloneMap(combination))
			return nil
		}
		key := order[index]
		for _, value := range axes[key] {
			combination[key] = value
			if err := expand(index+1, combination); err != nil {
				return err
			}
		}
		delete(combination, key)
		return nil
	}
	if len(axes) > 0 || len(include) == 0 {
		if err := expand(0, map[string]any{}); err != nil {
			return nil, err
		}
	}
	for _, addition := range include {
		applied := false
		for i, original := range originals {
			compatible := true
			for key, value := range addition {
				if old, exists := original[key]; exists && !reflect.DeepEqual(old, value) {
					compatible = false
					break
				}
			}
			if compatible {
				for key, value := range addition {
					jobs[i][key] = value
				}
				applied = true
			}
		}
		if !applied {
			if len(jobs) >= limit {
				return nil, tooManyJobs(limit + 1)
			}
			jobs = append(jobs, cloneMap(addition))
		}
	}
	return jobs, nil
}

func isExcludedSubtree(combination map[string]any, exclusions []map[string]any) bool {
	for _, exclusion := range exclusions {
		if containsCombination(combination, exclusion) {
			return true
		}
	}
	return false
}

func matrixObjects(value any, key string) ([]map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, expressionError("strategy.matrix."+key, "a list of mappings")
	}
	objects := make([]map[string]any, 0, len(list))
	for _, value := range list {
		object, ok := value.(map[string]any)
		if !ok {
			return nil, expressionError("strategy.matrix."+key, "a list of mappings")
		}
		objects = append(objects, object)
	}
	return objects, nil
}

func containsCombination(combination, subset map[string]any) bool {
	for key, value := range subset {
		old, ok := combination[key]
		if !ok || !reflect.DeepEqual(old, value) {
			return false
		}
	}
	return true
}

func cloneMap(source map[string]any) map[string]any {
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func tooManyJobs(count int) error {
	return refuse("workflow.too_many_jobs", "jobs", 0, fmt.Sprintf("This workflow would start %d jobs; OwnGit starts at most 16 for one run. Make the matrix smaller (an OS axis repeats the same work here), combine jobs, or move jobs to another workflow file.", count), map[string]string{"count": fmt.Sprint(count)})
}
