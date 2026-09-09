<a name="1.2.0"></a>

# 1.2.0 (2024-04-16)

## Breaking Changes

- Updated `PanicHandler`'s `HandlePanic` function signature to fix issues where panics were not handled correctly. When using the `HandlePanic` function you will need to call the `recover()` function yourselves in the deferred function to make go stop the panic. Please update your code that uses the `HandlePanic` function and pass it the result of running the `recover()` function.

- `ErrorHandler` constructor function name changed from `GetErrorHandler` to `NewErrorHandler` to adhere to golang standards.

<a name="1.1.0"></a>

# 1.1.0 (2024-03-20)

## New Features

- New `panic_handler` sub package has been added. You can use the new PanicHandler to recover from possible panics in your code. This is useful for long running applications that should not crash due to a panic.
