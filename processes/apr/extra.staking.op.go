package apr

import (
	"time"

	"github.com/yearn/ydaemon/common/bigNumber"
	"github.com/yearn/ydaemon/common/contracts"
	"github.com/yearn/ydaemon/common/ethereum"
	"github.com/yearn/ydaemon/common/helpers"
	"github.com/yearn/ydaemon/common/logs"
	"github.com/yearn/ydaemon/internal/models"
	"github.com/yearn/ydaemon/internal/multicalls"
	"github.com/yearn/ydaemon/internal/storage"
)

func computeOPBoostStakingRewardsAPR(chainID uint64, vault models.TVault) (*bigNumber.Float, bool) {
	/**********************************************************************************************
	** First thing to do is to check if the vault has a staking contract associated with it.
	** We can retrieve that from the store.
	**********************************************************************************************/
	stakingContract, ok := storage.GetOPStakingForVault(chainID, vault.Address)
	if !ok {
		return bigNumber.NewFloat(0), false
	}

	/**********************************************************************************************
	** Once we got it we will need a few things from the staking contract. We will use a multicall
	** to retrieve the following:
	** - The periodFinish
	** - The rewardRate
	** - The totalSupply
	**********************************************************************************************/
	calls := []ethereum.Call{}
	calls = append(calls, multicalls.GetPeriodFinish(stakingContract.StackingPoolAddress.Hex(), stakingContract.StackingPoolAddress))
	calls = append(calls, multicalls.GetRewardRate(stakingContract.StackingPoolAddress.Hex(), stakingContract.StackingPoolAddress))
	calls = append(calls, multicalls.GetTotalSupply(stakingContract.StackingPoolAddress.Hex(), stakingContract.StackingPoolAddress))
	calls = append(calls, multicalls.GetRewardsToken(stakingContract.StackingPoolAddress.Hex(), stakingContract.StackingPoolAddress))
	response := multicalls.Perform(chainID, calls, nil)
	periodFinish := helpers.DecodeBigInt(response[stakingContract.StackingPoolAddress.Hex()+`periodFinish`])
	rewardRateRaw := helpers.DecodeBigInt(response[stakingContract.StackingPoolAddress.Hex()+`rewardRate`])
	totalSupplyRaw := helpers.DecodeBigInt(response[stakingContract.StackingPoolAddress.Hex()+`totalSupply`])
	rewardsTokenRaw := helpers.DecodeAddress(response[stakingContract.StackingPoolAddress.Hex()+`rewardsToken`])

	/**********************************************************************************************
	** If periodFinish is before now, aka rewards are over, we can stop here
	**********************************************************************************************/
	now := time.Now().Unix()
	if periodFinish.Int64() < now {
		return bigNumber.NewFloat(0), false
	}

	/**********************************************************************************************
	** If the total supply is 0, we can stop here, aka nothing is staked, so no rewards
	**********************************************************************************************/
	if totalSupplyRaw.IsZero() {
		return bigNumber.NewFloat(0), false
	}

	/**********************************************************************************************
	** For the following steps, we will need to know the decimals of the vault token and the one
	** of the rewards token. The vault token decimals are already in the vault struct, but we will
	** need to retrieve the rewards token decimals.
	** If we already have this token loaded, this will be a simple lookup in the store. Otherwise,
	** we will need to load it from the blockchain.
	**********************************************************************************************/
	rewardsTokenDecimals := uint64(18)
	rewardsToken, ok := storage.GetERC20(chainID, rewardsTokenRaw)
	if !ok {
		erc20Contract, _ := contracts.NewERC20(rewardsTokenRaw, ethereum.GetRPC(chainID))
		decimals, err := erc20Contract.Decimals(nil)
		if err != nil {
			logs.Error(`Failed to retrieve decimals for ` + rewardsTokenRaw.Hex())
		} else {
			rewardsTokenDecimals = uint64(decimals)
		}
	} else {
		rewardsTokenDecimals = rewardsToken.Decimals
	}

	vaultToken, ok := storage.GetERC20(vault.ChainID, vault.Address)
	if !ok {
		return bigNumber.NewFloat(0), false
	}

	/**********************************************************************************************
	** If that's good, we will need the price of the vault token and the price of the rewards token
	** to compute the APR.
	**********************************************************************************************/
	vaultPrice := bigNumber.NewFloat(0)
	if tokenPrice, ok := storage.GetPrice(vault.ChainID, vault.Address); ok {
		vaultPrice = tokenPrice.HumanizedPrice
	}

	rewardsPrice := bigNumber.NewFloat(0)
	if tokenPrice, ok := storage.GetPrice(vault.ChainID, rewardsTokenRaw); ok {
		rewardsPrice = tokenPrice.HumanizedPrice
	}

	/**********************************************************************************************
	** Then, we need to scale the decimals of the rewardRate and the totalSupply to match the
	** decimals of the vault.
	**********************************************************************************************/
	rewardRate := helpers.ToNormalizedAmount(rewardRateRaw, rewardsTokenDecimals)
	totalSupply := helpers.ToNormalizedAmount(totalSupplyRaw, vaultToken.Decimals)
	perStakingTokenRate := bigNumber.NewFloat(0).Div(rewardRate, totalSupply)
	secondsPerYear := bigNumber.NewFloat(31_556_952)

	/**********************************************************************************************
	** Finally, we can compute the APR
	**********************************************************************************************/
	stakingRewardAPR := bigNumber.NewFloat(0).Mul(secondsPerYear, perStakingTokenRate)
	stakingRewardAPR = bigNumber.NewFloat(0).Mul(stakingRewardAPR, rewardsPrice)
	stakingRewardAPR = bigNumber.NewFloat(0).Div(stakingRewardAPR, vaultPrice)
	return stakingRewardAPR, true
}
