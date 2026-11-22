/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { CHANNEL_TYPE_NEW_API, CHANNEL_TYPE_SUB2API } from '../constants'

/**
 * Channel types whose upstream calls a non-streaming Chat Completions API.
 * Mirrors the backend fake-stream allow list; the per-channel switch decides
 * whether the feature is actually used.
 */
const FAKE_STREAM_CHANNEL_TYPES: ReadonlySet<number> = new Set([
  CHANNEL_TYPE_SUB2API,
  CHANNEL_TYPE_NEW_API,
])

export function supportsFakeStream(channelType: number): boolean {
  return FAKE_STREAM_CHANNEL_TYPES.has(channelType)
}
