/*
 * Copyright (c) 2020 Alex <alex@webz.asia>
 *
 *  Licensed under the Apache License, Version 2.0 (the "License");
 *  you may not use this file except in compliance with the License.
 *  You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 *  Unless required by applicable law or agreed to in writing, software
 *  distributed under the License is distributed on an "AS IS" BASIS,
 *  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *  See the License for the specific language governing permissions and
 *  limitations under the License.
 *
 *   @author Alex <alex@webz.asia>
 *   @copyright Copyright (c) 2020 Alex <alex@webz.asia>
 *   @since 03.10.2020
 *
 */

package ldflags

type buildInfo struct {
	version string
	hash    string
	time    string
}

var (
	buildVersion = "unknown"
	buildHash    = "unknown"
	buildTime    = "unknown"
	info         *buildInfo
)

func init() {
	info = New()
}

// New works as a singleton
func New() *buildInfo {
	if info == nil {
		info = &buildInfo{
			buildVersion, buildHash, buildTime,
		}
	}
	return info
}

// Version returns build version if provided, "unknown" otherwise
func Version() string {
	return info.version
}

// Build returns build hash if provided, "unknown" otherwise
func Build() string {
	return info.hash
}

// Time returns build time if provided, "unknown" otherwise
func Time() string {
	return info.time
}
