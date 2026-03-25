package pcl

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// VariableProvider handles both raw data ($) and formatted macros (@).
// args will be populated if the syntax @MACRO(arg1, arg2) is used.
type VariableProvider func(args ...string) interface{}

// PCLContext maps keys (including $ or @ prefix) to provider functions.
type PCLContext map[string]VariableProvider

// ProcessPhrase is the high-level entry point for the PCL engine.
func ProcessPhrase(input string, ctx PCLContext) (string, error) {
	// 1. Initial Pass: Expand all {$VAR}, {@MACRO}, or standalone $VAR tokens.
	// This regex captures: 1:Prefix($/@), 2:Name, 3:Args(optional)
	tokenRegex := regexp.MustCompile(`\{?([$@])([A-Z0-9_]+)(?:\((.*?)\))?\}?`)
	cache := make(map[string]string)

	resolved := tokenRegex.ReplaceAllStringFunc(input, func(fullMatch string) string {
		match := tokenRegex.FindStringSubmatch(fullMatch)
		if len(match) < 3 {
			return fullMatch
		}

		prefix := match[1]
		varName := match[2]
		argString := match[3]
		lookupKey := prefix + varName

		if provider, ok := ctx[lookupKey]; ok {
			var args []string
			if argString != "" {
				args = strings.Split(argString, ",")
				for i := range args {
					args[i] = strings.TrimSpace(args[i])
				}
			}
			
			val := fmt.Sprintf("%v", provider(args...))
			
			// Cache using the full match string to distinguish @MACRO(1) from @MACRO(2)
			cache[fullMatch] = val
			// Also cache the bare variable name for use in WHEN logic comparisons
			cache[prefix+varName] = val 
			
			return val
		}
		return fullMatch
	})

	// 2. Second Pass: Process Logic Blocks {WHEN ...} or {SAY ...}
	// Handles nested braces by finding the innermost or sequential blocks.
	pclRegex := regexp.MustCompile(`\{[^{}]*(?:\{[^{}]*\}[^{}]*)*\}`)
	
	for {
		match := pclRegex.FindString(resolved)
		if match == "" {
			break
		}
		// Strip outer braces and execute logic
		content := match[1 : len(match)-1]
		replacement := executePCL(content, ctx, cache)
		resolved = strings.Replace(resolved, match, replacement, 1)
	}

	// Final cleanup of extra whitespace and joining
	return strings.Join(strings.Fields(resolved), " "), nil
}

// executePCL determines if a block is a conditional or a direct instruction.
func executePCL(statement string, ctx PCLContext, cache map[string]string) string {
	statement = strings.TrimSpace(statement)
	tokens := tokenizePCL(statement)
	if len(tokens) == 0 {
		return ""
	}

	// Case: Explicit {SAY `text` or $VAR}
	if tokens[0] == "SAY" {
		if len(tokens) > 1 {
			return fmt.Sprintf("%v", resolveValue(tokens[1], ctx, cache))
		}
		return ""
	}

	// Case: Implied SAY via raw injection {$VAR} or {@MACRO}
	if strings.HasPrefix(tokens[0], "$") || strings.HasPrefix(tokens[0], "@") {
		return fmt.Sprintf("%v", resolveValue(tokens[0], ctx, cache))
	}

	// Case: Conditional {WHEN ... SAY ... OTHERWISE ...}
	if tokens[0] == "WHEN" {
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

		// Find OTHERWISE if it exists
		otherwiseIdx := -1
		for i := sayIdx; i < len(tokens); i++ {
			if tokens[i] == "OTHERWISE" {
				otherwiseIdx = i
				break
			}
		}

		if conditionMet {
			return strings.Trim(tokens[sayIdx+1], "`")
		} else if otherwiseIdx != -1 {
			nextPart := tokens[otherwiseIdx+1]
			// Handle nested OTHERWISE {WHEN...}
			if strings.HasPrefix(nextPart, "{") {
				return executePCL(nextPart[1:len(nextPart)-1], ctx, cache)
			}
			// Handle OTHERWISE SAY `...`
			if nextPart == "SAY" && len(tokens) > otherwiseIdx+2 {
				return strings.Trim(tokens[otherwiseIdx+2], "`")
			}
		}
	}

	return ""
}

// resolveValue pulls from the cache or invokes a provider for a specific token.
func resolveValue(token string, ctx PCLContext, cache map[string]string) interface{} {
	// Clean backticks
	clean := strings.Trim(token, "`")
	
	if strings.HasPrefix(clean, "$") || strings.HasPrefix(clean, "@") {
		if val, ok := cache[clean]; ok {
			return val
		}
		
		// Fallback regex for parameterized tokens inside WHEN blocks
		tokenRegex := regexp.MustCompile(`([$@])([A-Z0-9_]+)(?:\((.*?)\))?`)
		m := tokenRegex.FindStringSubmatch(clean)
		if len(m) > 2 {
			lookupKey := m[1] + m[2]
			if provider, ok := ctx[lookupKey]; ok {
				var args []string
				if m[3] != "" {
					args = strings.Split(m[3], ",")
				}
				return provider(args...)
			}
		}
	}
	return clean
}

// evaluateComplexCondition handles logical AND/OR comparisons.
func evaluateComplexCondition(tokens []string, ctx PCLContext, cache map[string]string) bool {
	if len(tokens) == 0 { return false }
	result := false
	currentOp := "OR"

	for i := 0; i < len(tokens); i += 4 {
		if i+2 >= len(tokens) { break }

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

// tokenizePCL splits the statement while respecting backticks and nested braces.
func tokenizePCL(s string) []string {
	var tokens []string
	var current strings.Builder
	inQuotes := false
	depth := 0
	for i := 0; i < len(s); i++ {
		char := s[i]
		
		// Use backticks for quoting to allow apostrophes like don't
		if char == '`' { 
			inQuotes = !inQuotes 
			current.WriteByte(char)
			continue
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
	if current.Len() > 0 { tokens = append(tokens, current.String()) }
	return tokens
}