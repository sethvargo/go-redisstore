package redisstore

const luaTemplate = `
local C_EXPIRE   = 'EXPIRE'
local C_HGETALL  = 'HGETALL'
local C_HSET     = 'HSET'
local F_START    = 's'
local F_TICK     = 't'
local F_INTERVAL = 'i'
local F_TOKENS   = 'k'
local F_MAX      = 'm'

-- speed up access to next
local next = next

-- get arguments
local key          = KEYS[1]
local now          = tonumber(ARGV[1]) -- current unix time in nanoseconds
local defmaxtokens = tonumber(ARGV[2]) -- default tokens per interval, only used if no value already exists for the key
local definterval  = tonumber(ARGV[3]) -- interval in nanoseconds, only used if no value already exists for the key

-- hgetall gets all the fields as a lua table.
local hgetall = function (key)
  local data = redis.call(C_HGETALL, key)
  local result = {}
  for i = 1, #data, 2 do
    result[data[i]] = data[i+1]
  end
  return result
end

-- availabletokens returns the number of available tokens given the last tick,
-- current tick, max, and fill rate.
local availabletokens = function (last, curr, max, fillrate)
  local delta = curr - last
  local available = delta * fillrate
  if available > max then
    available = max
  end
  return available
end

-- present returns true if the given value is not nil and is not the empty
-- string.
local present = function (val)
	return val ~= nil and val ~= ''
end

-- tick returns the total number of times the interval has occurred between
-- start and current.
local tick = function (start, curr, interval)
  local val = math.floor((curr - start) / interval)
	if val > 0 then
		return val
	end
	return 0
end

-- ttl returns the appropriate ttl in seconds for the given interval, 3x the
-- interval.
local ttl = function (interval)
  return 3 * math.floor(interval / 1000000000)
end


--
-- begin exec
--

local data = hgetall(key)

local start = now
if present(data[F_START]) then
	start = tonumber(data[F_START])
else
	redis.call(C_HSET, key, F_START, now)
	redis.call(C_EXPIRE, key, 30)
end

local lasttick = 0
if present(data[F_TICK]) then
	lasttick = tonumber(data[F_TICK])
else
	redis.call(C_HSET, key, F_TICK, 0)
	redis.call(C_EXPIRE, key, 30)
end

local maxtokens = defmaxtokens
if present(data[F_MAX]) then
	maxtokens = tonumber(data[F_MAX])
end

local tokens = maxtokens
if present(data[F_TOKENS]) then
	tokens = tonumber(data[F_TOKENS])
end

local interval = definterval
if present(data[F_INTERVAL]) then
	interval = tonumber(data[F_INTERVAL])
end

local currtick = tick(start, now, interval)
local nexttime = start + ((currtick+1) * interval)

if lasttick < currtick then
	local rate = interval / tokens
  tokens = availabletokens(lasttick, currtick, maxtokens, rate)
  lasttick = currtick
  redis.call(C_HSET, key,
		F_START, start,
		F_TICK, lasttick,
		F_INTERVAL, interval,
		F_TOKENS, tokens)
	redis.call(C_EXPIRE, key, ttl(interval))
end

if tokens > 0 then
  tokens = tokens - 1
  redis.call(C_HSET, key, F_TOKENS, tokens)
	redis.call(C_EXPIRE, key, ttl(interval))
  return {maxtokens, tokens, nexttime, true}
end

return {maxtokens, tokens, nexttime, false}
`
