package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTestCtx() *EvalContext {
	return &EvalContext{
		Github: map[string]interface{}{
			"sha":        "abc123def456",
			"ref":        "refs/heads/main",
			"ref_name":   "main",
			"actor":      "octocat",
			"event_name": "push",
			"repository": "owner/repo",
		},
		Env: map[string]string{
			"MY_VAR": "hello",
		},
		Job: map[string]interface{}{
			"status": "success",
		},
		Steps: map[string]StepResult{
			"foo": {
				Outcome:    "success",
				Conclusion: "success",
				Outputs:    map[string]string{"bar": "baz"},
			},
			"setup": {
				Outcome:    "failure",
				Conclusion: "failure",
				Outputs:    map[string]string{},
			},
		},
		Runner: map[string]interface{}{
			"os":   "Linux",
			"arch": "X64",
		},
		Secrets:   map[string]string{"MY_SECRET": "s3cr3t"},
		Inputs:    map[string]string{},
		JobStatus: JobStatusSuccess,
		Cancelled: false,
	}
}

var eval = &Evaluator{}

// ---- Literals ----

func TestEval_StringLiteral(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("'hello'", ctx)
	require.NoError(t, err)
	assert.Equal(t, "hello", val)
}

func TestEval_EmptyStringLiteral(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("''", ctx)
	require.NoError(t, err)
	assert.Equal(t, "", val)
}

func TestEval_BoolLiteralTrue(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("true", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_BoolLiteralFalse(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("false", ctx)
	require.NoError(t, err)
	assert.Equal(t, false, val)
}

func TestEval_NumberLiteral(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("42", ctx)
	require.NoError(t, err)
	assert.Equal(t, float64(42), val)
}

func TestEval_NullLiteral(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("null", ctx)
	require.NoError(t, err)
	assert.Nil(t, val)
}

// ---- Property access ----

func TestEval_PropertyAccess_Simple(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("github.sha", ctx)
	require.NoError(t, err)
	assert.Equal(t, "abc123def456", val)
}

func TestEval_PropertyAccess_RefName(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("github.ref_name", ctx)
	require.NoError(t, err)
	assert.Equal(t, "main", val)
}

func TestEval_PropertyAccess_Nested_StepOutputs(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("steps.foo.outputs.bar", ctx)
	require.NoError(t, err)
	assert.Equal(t, "baz", val)
}

func TestEval_PropertyAccess_StepOutcome(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("steps.foo.outcome", ctx)
	require.NoError(t, err)
	assert.Equal(t, "success", val)
}

func TestEval_PropertyAccess_MissingKey(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("github.nonexistent", ctx)
	require.NoError(t, err)
	assert.Nil(t, val)
}

func TestEval_PropertyAccess_EnvVar(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("env.MY_VAR", ctx)
	require.NoError(t, err)
	assert.Equal(t, "hello", val)
}

func TestEval_PropertyAccess_Secret(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("secrets.MY_SECRET", ctx)
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t", val)
}

// ---- Array index ----

func TestEval_ArrayIndex(t *testing.T) {
	ctx := makeTestCtx()
	ctx.Github["items"] = []interface{}{"first", "second", "third"}
	val, err := eval.Eval("github.items[1]", ctx)
	require.NoError(t, err)
	assert.Equal(t, "second", val)
}

// ---- Comparisons ----

func TestEval_CompareEQ_True(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("github.ref == 'refs/heads/main'", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_CompareEQ_False(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("github.ref == 'refs/heads/dev'", ctx)
	require.NoError(t, err)
	assert.Equal(t, false, val)
}

func TestEval_CompareNEQ(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("github.ref != 'refs/heads/dev'", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_CompareEQ_CaseInsensitive(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("'Hello' == 'hello'", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_CompareNumbers(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("42 > 10", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_CompareLTE(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("5 <= 5", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

// ---- Logical operators ----

func TestEval_LogicalAnd_TrueTrue(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("true && true", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_LogicalAnd_TrueFalse(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("true && false", ctx)
	require.NoError(t, err)
	assert.Equal(t, false, val)
}

func TestEval_LogicalOr_FalseTrue(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("false || true", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_LogicalOr_FalseFalse(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("false || false", ctx)
	require.NoError(t, err)
	assert.Equal(t, false, val)
}

func TestEval_Not_True(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("!true", ctx)
	require.NoError(t, err)
	assert.Equal(t, false, val)
}

func TestEval_Not_False(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("!false", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_Not_Null(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("!null", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

// ---- Combined expressions ----

func TestEval_SuccessAndRef(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("success() && github.ref == 'refs/heads/main'", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_SuccessAndRef_WrongRef(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("success() && github.ref == 'refs/heads/dev'", ctx)
	require.NoError(t, err)
	assert.Equal(t, false, val)
}

func TestEval_NotCancelled(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("!cancelled()", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_NotCancelled_WhenCancelled(t *testing.T) {
	ctx := makeTestCtx()
	ctx.Cancelled = true
	val, err := eval.Eval("!cancelled()", ctx)
	require.NoError(t, err)
	assert.Equal(t, false, val)
}

func TestEval_Parens(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("(true || false) && true", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

// ---- Built-in functions ----

func TestEval_Contains_StringTrue(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("contains(github.ref, 'main')", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_Contains_StringFalse(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("contains(github.ref, 'dev')", ctx)
	require.NoError(t, err)
	assert.Equal(t, false, val)
}

func TestEval_Contains_CaseInsensitive(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("contains('Hello World', 'hello')", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_Contains_Array(t *testing.T) {
	ctx := makeTestCtx()
	ctx.Github["labels"] = []interface{}{"bug", "enhancement", "help wanted"}
	val, err := eval.Eval("contains(github.labels, 'bug')", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_Contains_ArrayMissing(t *testing.T) {
	ctx := makeTestCtx()
	ctx.Github["labels"] = []interface{}{"enhancement"}
	val, err := eval.Eval("contains(github.labels, 'bug')", ctx)
	require.NoError(t, err)
	assert.Equal(t, false, val)
}

func TestEval_StartsWith_True(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("startsWith(github.ref, 'refs/heads/')", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_StartsWith_False(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("startsWith(github.ref, 'refs/tags/')", ctx)
	require.NoError(t, err)
	assert.Equal(t, false, val)
}

func TestEval_StartsWith_CaseInsensitive(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("startsWith('REFS/HEADS/main', 'refs/heads/')", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_EndsWith_True(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("endsWith(github.ref, '/main')", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_EndsWith_False(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("endsWith(github.ref, '/dev')", ctx)
	require.NoError(t, err)
	assert.Equal(t, false, val)
}

func TestEval_Format(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("format('Hello {0}!', github.actor)", ctx)
	require.NoError(t, err)
	assert.Equal(t, "Hello octocat!", val)
}

func TestEval_Format_MultipleArgs(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("format('{0}/{1}', 'owner', 'repo')", ctx)
	require.NoError(t, err)
	assert.Equal(t, "owner/repo", val)
}

func TestEval_Join_Array(t *testing.T) {
	ctx := makeTestCtx()
	ctx.Github["tags"] = []interface{}{"v1", "v2", "v3"}
	val, err := eval.Eval("join(github.tags, ', ')", ctx)
	require.NoError(t, err)
	assert.Equal(t, "v1, v2, v3", val)
}

func TestEval_Join_String(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("join('hello', ',')", ctx)
	require.NoError(t, err)
	assert.Equal(t, "hello", val)
}

func TestEval_ToJSON(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("toJSON('hello')", ctx)
	require.NoError(t, err)
	assert.Equal(t, `"hello"`, val)
}

func TestEval_FromJSON(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("fromJSON('{\"key\":\"value\"}')", ctx)
	require.NoError(t, err)
	m, ok := val.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "value", m["key"])
}

// ---- Status functions ----

func TestEval_Always_ReturnsTrue(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("always()", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_Always_ReturnsTrueOnFailure(t *testing.T) {
	ctx := makeTestCtx()
	ctx.JobStatus = JobStatusFailure
	val, err := eval.Eval("always()", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_Always_ReturnsTrueOnCancelled(t *testing.T) {
	ctx := makeTestCtx()
	ctx.Cancelled = true
	val, err := eval.Eval("always()", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_Success_WhenSuccess(t *testing.T) {
	ctx := makeTestCtx()
	ctx.JobStatus = JobStatusSuccess
	val, err := eval.Eval("success()", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_Success_WhenPending(t *testing.T) {
	ctx := makeTestCtx()
	ctx.JobStatus = JobStatusPending
	val, err := eval.Eval("success()", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_Success_WhenFailure(t *testing.T) {
	ctx := makeTestCtx()
	ctx.JobStatus = JobStatusFailure
	val, err := eval.Eval("success()", ctx)
	require.NoError(t, err)
	assert.Equal(t, false, val)
}

func TestEval_Failure_WhenFailure(t *testing.T) {
	ctx := makeTestCtx()
	ctx.JobStatus = JobStatusFailure
	val, err := eval.Eval("failure()", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_Failure_WhenSuccess(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("failure()", ctx)
	require.NoError(t, err)
	assert.Equal(t, false, val)
}

func TestEval_Cancelled_WhenCancelled(t *testing.T) {
	ctx := makeTestCtx()
	ctx.Cancelled = true
	val, err := eval.Eval("cancelled()", ctx)
	require.NoError(t, err)
	assert.Equal(t, true, val)
}

func TestEval_Cancelled_WhenNotCancelled(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("cancelled()", ctx)
	require.NoError(t, err)
	assert.Equal(t, false, val)
}

func TestEval_HashFiles_ReturnsEmpty(t *testing.T) {
	ctx := makeTestCtx()
	val, err := eval.Eval("hashFiles('**/*.go')", ctx)
	require.NoError(t, err)
	assert.Equal(t, "", val)
}

// ---- EvalBool ----

func TestEvalBool_WithWrapper(t *testing.T) {
	ctx := makeTestCtx()
	result, err := eval.EvalBool("${{ github.ref == 'refs/heads/main' }}", ctx)
	require.NoError(t, err)
	assert.True(t, result)
}

func TestEvalBool_WithoutWrapper(t *testing.T) {
	ctx := makeTestCtx()
	result, err := eval.EvalBool("github.ref == 'refs/heads/main'", ctx)
	require.NoError(t, err)
	assert.True(t, result)
}

func TestEvalBool_NonEmptyStringIsTruthy(t *testing.T) {
	ctx := makeTestCtx()
	result, err := eval.EvalBool("github.sha", ctx)
	require.NoError(t, err)
	assert.True(t, result)
}

func TestEvalBool_EmptyStringIsFalsy(t *testing.T) {
	ctx := makeTestCtx()
	result, err := eval.EvalBool("''", ctx)
	require.NoError(t, err)
	assert.False(t, result)
}

func TestEvalBool_NullIsFalsy(t *testing.T) {
	ctx := makeTestCtx()
	result, err := eval.EvalBool("null", ctx)
	require.NoError(t, err)
	assert.False(t, result)
}

func TestEvalBool_ZeroIsFalsy(t *testing.T) {
	ctx := makeTestCtx()
	result, err := eval.EvalBool("0", ctx)
	require.NoError(t, err)
	assert.False(t, result)
}

func TestEvalBool_FalseIsFalsy(t *testing.T) {
	ctx := makeTestCtx()
	result, err := eval.EvalBool("false", ctx)
	require.NoError(t, err)
	assert.False(t, result)
}

func TestEvalBool_IfFieldWithSuccess(t *testing.T) {
	ctx := makeTestCtx()
	result, err := eval.EvalBool("success() && github.ref == 'refs/heads/main'", ctx)
	require.NoError(t, err)
	assert.True(t, result)
}

// ---- Interpolation ----

func TestInterpolate_Single(t *testing.T) {
	ctx := makeTestCtx()
	result := eval.Interpolate("Branch: ${{ github.ref_name }}", ctx)
	assert.Equal(t, "Branch: main", result)
}

func TestInterpolate_Multiple(t *testing.T) {
	ctx := makeTestCtx()
	result := eval.Interpolate("${{ github.actor }} pushed to ${{ github.ref_name }}", ctx)
	assert.Equal(t, "octocat pushed to main", result)
}

func TestInterpolate_NoExpr(t *testing.T) {
	ctx := makeTestCtx()
	result := eval.Interpolate("no expressions here", ctx)
	assert.Equal(t, "no expressions here", result)
}

func TestInterpolate_ExprWithFunction(t *testing.T) {
	ctx := makeTestCtx()
	result := eval.Interpolate("Hello ${{ format('{0}!', github.actor) }}", ctx)
	assert.Equal(t, "Hello octocat!", result)
}

func TestInterpolate_BoolValue(t *testing.T) {
	ctx := makeTestCtx()
	result := eval.Interpolate("Is main: ${{ github.ref == 'refs/heads/main' }}", ctx)
	assert.Equal(t, "Is main: true", result)
}

func TestInterpolate_WithSpaces(t *testing.T) {
	ctx := makeTestCtx()
	result := eval.Interpolate("SHA: ${{  github.sha  }}", ctx)
	assert.Equal(t, "SHA: abc123def456", result)
}
