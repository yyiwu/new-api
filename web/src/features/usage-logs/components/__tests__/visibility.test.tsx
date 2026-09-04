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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, test } from 'vitest'

import type { UsageLog } from '../../data/schema'
import { formatModelName } from '../../lib/format'
import { DetailsDialog } from '../dialogs/details-dialog'

const queryClients: QueryClient[] = []

function makeLog(other: Record<string, unknown>): UsageLog {
  return {
    id: 1,
    user_id: 1,
    created_at: 1,
    type: 2,
    content: '',
    username: 'user',
    token_name: 'token',
    model_name: 'requested-model',
    quota: 0,
    prompt_tokens: 0,
    completion_tokens: 0,
    use_time: 0,
    is_stream: false,
    channel: 1,
    channel_name: 'channel',
    token_id: 1,
    group: 'default',
    ip: '',
    other: JSON.stringify(other),
    request_id: 'req-1',
    upstream_request_id: '',
  }
}

function renderDetails(other: Record<string, unknown>, isAdmin: boolean): void {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  queryClient.setQueryData(
    ['status'],
    {},
    { updatedAt: Date.now() + 60_000 }
  )
  queryClients.push(queryClient)

  render(
    <QueryClientProvider client={queryClient}>
      <DetailsDialog
        log={makeLog(other)}
        isAdmin={isAdmin}
        isRoot={false}
        open
        onOpenChange={() => undefined}
      />
    </QueryClientProvider>
  )
}

afterEach(() => {
  for (const queryClient of queryClients) queryClient.clear()
  queryClients.length = 0
})

describe('usage log routing and override visibility', () => {
  test('formats routed model metadata only for admins', () => {
    const log = makeLog({
      admin_info: {
        is_model_mapped: true,
        upstream_model_name: 'routed-secret-model',
      },
    })

    expect(formatModelName(log)).toEqual({
      name: 'requested-model',
      isMapped: false,
      actualModel: undefined,
    })
    expect(formatModelName(log, true)).toEqual({
      name: 'requested-model',
      isMapped: true,
      actualModel: 'routed-secret-model',
    })
  })

  test('shows requested effort but hides legacy sensitive metadata from users', () => {
    renderDetails(
      {
        reasoning_effort: 'low',
        is_model_mapped: true,
        upstream_model_name: 'routed-secret-model',
        is_system_prompt_overwritten: true,
        po: ['set reasoning.effort = high'],
      },
      false
    )

    expect(screen.getByText('low')).toBeInTheDocument()
    expect(screen.queryByText('Actual Model')).toBeNull()
    expect(screen.queryByText('routed-secret-model')).toBeNull()
    expect(screen.queryByText('Overwritten')).toBeNull()
    expect(screen.queryByText(/Param Override/)).toBeNull()
  })

  test('shows scoped routing and override metadata to admins', () => {
    renderDetails(
      {
        reasoning_effort: 'low',
        admin_info: {
          is_model_mapped: true,
          upstream_model_name: 'routed-secret-model',
          is_system_prompt_overwritten: true,
          po: ['set reasoning.effort = high'],
        },
      },
      true
    )

    expect(screen.getByText('low')).toBeInTheDocument()
    expect(screen.getByText('routed-secret-model')).toBeInTheDocument()
    expect(screen.getByText('Overwritten')).toBeInTheDocument()
    expect(screen.getByText(/Param Override/)).toBeInTheDocument()
  })
})
