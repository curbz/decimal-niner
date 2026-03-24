package pcl

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// VariableProvider is a function that returns the value for a PCL variable.
// This allows for lazy evaluation of expensive calculations.
type VariableProvider func() interface{}

// PCLContext maps variable names (without the $) to their provider functions.
type PCLContext map[string]VariableProvider

// ProcessPhrase is the main entry point. It resolves variables and executes PCL blocks.
func ProcessPhrase(input string, ctx PCLContext) (string, error) {
	// 1. Identify which variables are actually present in the string to invoke providers lazily.
	varRegex := regexp.MustCompile(`\$([A-Z0-9_]+)`)
	matches := varRegex.FindAllStringSubmatch(input, -1)

	resolved := input
	// Create a local cache for this execution so multiple uses of the same $VAR
	// in one phrase only call the provider once.
	cache := make(map[string]string)

	for _, match := range matches {
		varName := match[1]
		if provider, ok := ctx[varName]; ok {
			if _, cached := cache[varName]; !cached {
				cache[varName] = fmt.Sprintf("%v", provider())
			}
			resolved = strings.ReplaceAll(resolved, "$"+varName, cache[varName])
		}
	}

	// 2. Recursive PCL logic: Find and replace {WHEN ...} blocks.
	// This regex handles nested braces by finding the innermost or sequential blocks.
	pclRegex := regexp.MustCompile(`\{WHEN\s+[^{}]*(?:\{[^{}]*\}[^{}]*)*\}`)
	
	for {
		match := pclRegex.FindString(resolved)
		if match == "" {
			break
		}
		// Strip the outer braces and execute
		content := match[1 : len(match)-1]
		replacement := executePCL(content, ctx, cache)
		resolved = strings.Replace(resolved, match, replacement, 1)
	}

	// Final cleanup of whitespace
	return strings.Join(strings.Fields(resolved), " "), nil
}

// executePCL parses and evaluates a single {WHEN ...} statement.
func executePCL(statement string, ctx PCLContext, cache map[string]string) string {
	tokens := tokenizePCL(statement)
	if len(tokens) < 4 || tokens[0] != "WHEN" {
		return ""
	}

	// Find SAY to isolate the boolean condition
	sayIdx := -1
	for i, t := range tokens {
		if t == "SAY" {
			sayIdx = i
			break
		}
	}
	if sayIdx == -1 {
		return ""
	}

	conditionMet := evaluateComplexCondition(tokens[1:sayIdx], ctx, cache)

	// Find OTHERWISE
	otherwiseIdx := -1
	for i := sayIdx; i < len(tokens); i++ {
		if tokens[i] == "OTHERWISE" {
			otherwiseIdx = i
			break
		}
	}

	if conditionMet {
		return strings.Trim(tokens[sayIdx+1], "'")
	} else if otherwiseIdx != -1 {
		nextPart := tokens[otherwiseIdx+1]
		// If nested PCL: {WHEN...}
		if strings.HasPrefix(nextPart, "{") {
			return executePCL(nextPart[1:len(nextPart)-1], ctx, cache)
		}
		// If simple SAY
		if nextPart == "SAY" {
			return strings.Trim(tokens[otherwiseIdx+2], "'")
		}
	}
	return ""
}

// evaluateComplexCondition handles AND/OR logic.
func evaluateComplexCondition(tokens []string, ctx PCLContext, cache map[string]string) bool {
	if len(tokens) == 0 {
		return false
	}

	result := false
	currentOp := "OR" // First expression sets the initial result

	for i := 0; i < len(tokens); i += 4 {
		if i+2 >= len(tokens) {
			break
		}

		left := resolveValue(tokens[i], ctx, cache)
		op := tokens[i+1]
		right := resolveValue(tokens[i+2], ctx, cache)

		match := evaluateComparison(left, op, right)

		if currentOp == "AND" {
			result = result && match
		} else {
			result = result || match
		}

		if i+3 < len(tokens) {
			currentOp = tokens[i+3]
		}
	}
	return result
}

// evaluateComparison performs numeric or string comparison.
func evaluateComparison(left interface{}, op string, right interface{}) bool {
	lStr := fmt.Sprintf("%v", left)
	rStr := fmt.Sprintf("%v", right)
	lVal, errL := strconv.ParseFloat(lStr, 64)
	rVal, errR := strconv.ParseFloat(rStr, 64)

	if errL == nil && errR == nil {
		switch op {
		case "EQ": return lVal == rVal
		case "LT": return lVal < rVal
		case "LE": return lVal <= rVal
		case "GT": return lVal > rVal
		case "GE": return lVal >= rVal
		}
	}
	return lStr == rStr
}

// resolveValue pulls from cache or provider lazily.
func resolveValue(token string, ctx PCLContext, cache map[string]string) interface{} {
	if strings.HasPrefix(token, "$") {
		varName := strings.TrimPrefix(token, "$")
		if val, ok := cache[varName]; ok {
			return val
		}
		if provider, ok := ctx[varName]; ok {
			val := fmt.Sprintf("%v", provider())
			cache[varName] = val
			return val
		}
	}
	return strings.Trim(token, "'")
}

// tokenizePCL splits the statement while respecting quotes and nested braces.
func tokenizePCL(s string) []string {
	var tokens []string
	var current strings.Builder
	inQuotes := false
	depth := 0
	for i := 0; i < len(s); i++ {
		char := s[i]
		if char == '\'' {
			inQuotes = !inQuotes
		}
		if !inQuotes {
			if char == '{' { depth++ }
			if char == '}' { depth-- }
		}
		if char == ' ' && !inQuotes && depth == 0 {
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		} else {
			current.WriteByte(char)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}
